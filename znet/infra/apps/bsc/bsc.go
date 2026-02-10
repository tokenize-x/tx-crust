package bsc

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/go-bip39"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"

	"github.com/tokenize-x/tx-crust/znet/infra"
	"github.com/tokenize-x/tx-crust/znet/infra/targets"
	"github.com/tokenize-x/tx-tools/pkg/must"
	"github.com/tokenize-x/tx-tools/pkg/retry"
)

var (
	//go:embed run.tmpl
	scriptTmpl        string
	runScriptTemplate = template.Must(template.New("").Parse(scriptTmpl))

	//go:embed genesis.tmpl
	genesisTmplStr  string
	genesisTemplate = template.Must(template.New("").Parse(genesisTmplStr))
)

const (
	AppType infra.AppType = "bsc"

	DefaultRPCPort = 8545 // HTTP JSON‑RPC
	DefaultWSPort  = 8546 // WebSocket

	dockerEntrypoint   = "run.sh"
	genesisFileName    = "genesis.json"
	privateKeyFileName = "private.key"
	passwordFileName   = "pwd.txt"
)

// Config stores Binance Smart Chain (BSC) app config.
type Config struct {
	Name             string
	HomeDir          string         // host directory that will be mounted into the container
	AppInfo          *infra.AppInfo // infra meta‑data (host, ports, status, etc.)
	RPCPort          int
	WSPort           int
	ChainID          int64 // Binance mainnet = 56; testnet = 97; for local use any >1 works.
	FaucetAddr       string
	FaucetPrivateKey []byte
}

// New creates a new Binance Smart Chain (BSC) app instance.
func New(cfg Config) BSC {
	return BSC{config: cfg}
}

// BSC represents a single‑node Binance Smart Chain deployment.
type BSC struct {
	config Config
}

// Type implements infra.App.
func (b BSC) Type() infra.AppType { return AppType }

// Name implements infra.App.
func (b BSC) Name() string { return b.config.Name }

// Info implements infra.App.
func (b BSC) Info() infra.DeploymentInfo { return b.config.AppInfo.Info() }

// Config returns the raw config struct.
func (b BSC) Config() Config { return b.config }

// HealthCheck pings the node with `eth_blockNumber`.
func (b BSC) HealthCheck(ctx context.Context) error {
	if b.config.AppInfo.Info().Status != infra.AppStatusRunning {
		return retry.Retryable(errors.Errorf("BSC hasn't started yet"))
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	rpcURL := infra.JoinNetAddr("http", b.Info().HostFromHost, b.config.RPCPort)

	reqBody := `{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`
	req := must.HTTPRequest(
		http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, strings.NewReader(reqBody)),
	)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return retry.Retryable(errors.WithStack(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return retry.Retryable(errors.Errorf("health check failed, status code: %d", resp.StatusCode))
	}

	var result struct {
		Result string `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Errorf("failed to read health‑check body: %v", err)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return errors.Wrap(err, "unmarshal health check response")
	}
	if result.Error != nil {
		return retry.Retryable(errors.Errorf("rpc error %d: %s", result.Error.Code, result.Error.Message))
	}
	// If we got a hex string back, the node is alive.
	if len(result.Result) == 0 || !strings.HasPrefix(result.Result, "0x") {
		return retry.Retryable(errors.New("invalid blockNumber response"))
	}
	return nil
}

// Deployment returns the infra.Deployment description used by the orchestrator.
func (b BSC) Deployment() infra.Deployment {
	return infra.Deployment{
		RunAsUser: true,
		Image:     "ghcr.io/bnb-chain/bsc:1.6.6", // official BSC geth image
		Name:      b.Name(),
		Info:      b.config.AppInfo,
		Volumes: []infra.Volume{
			{
				Source:      b.config.HomeDir,   // host path
				Destination: targets.AppHomeDir, // container mount point (/app)
			},
		},
		Ports: map[string]int{
			"rpc": b.config.RPCPort,
			"ws":  b.config.WSPort,
		},
		PrepareFunc: b.prepare,
		Entrypoint:  filepath.Join(targets.AppHomeDir, dockerEntrypoint),
	}
}

// prepare writes private key, genesis.json, password file and entrypoint.
func (b BSC) prepare(_ context.Context) error {
	if err := b.savePrivateKeyFile(); err != nil {
		return err
	}
	if err := b.saveGenesisFile(); err != nil {
		return err
	}
	if err := b.savePasswordFile(); err != nil {
		return err
	}
	return b.saveRunScriptFile()
}

// saveGenesisFile writes a Parlia PoA genesis using the template.
func (b BSC) saveGenesisFile() error {
	// Build the padded extraData field required by Parlia (Luban format):
	validatorAddr := strings.TrimPrefix(b.config.FaucetAddr, "0x")
	if len(validatorAddr) != 40 {
		return errors.Errorf("validator address must be 20 bytes hex: %s", b.config.FaucetAddr)
	}
	// Luban-format extraData:
	// 32 vanity bytes + 1 count byte + 20 address bytes + 48 BLS pubkey bytes + 65 seal bytes
	vanity := strings.Repeat("0", 64)  // 32 bytes in hex
	count := "01"                       // 1 validator
	blsKey := strings.Repeat("0", 96)  // 48 zero bytes (BLS pubkey placeholder)
	seal := strings.Repeat("0", 130)   // 65 zero bytes (seal placeholder)
	extraData := "0x" + vanity + count + validatorAddr + blsKey + seal

	genesisArgs := struct {
		ChainID   int64
		Validator string
		ExtraData string
	}{
		ChainID:   b.config.ChainID,
		Validator: strings.ToLower(b.config.FaucetAddr),
		ExtraData: extraData,
	}

	fpath := filepath.Join(b.config.HomeDir, genesisFileName)

	f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return errors.WithStack(err)
	}
	defer f.Close()

	if err := genesisTemplate.Execute(f, genesisArgs); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// savePasswordFile creates an empty password file (required by geth when unlocking).
func (b BSC) savePasswordFile() error {
	fpath := filepath.Join(b.config.HomeDir, passwordFileName)
	return os.WriteFile(fpath, []byte{}, 0o600)
}

func ExtractKeyPairsFromSeed(seedPhrase string) ([]byte, string, error) {
	seed := bip39.NewSeed(seedPhrase, keyring.DefaultBIP39Passphrase)
	masterPriv, ch := hd.ComputeMastersFromSeed(seed)
	hdPath := hd.CreateHDPath(60, 0, 0).String()
	privKey, err := hd.DerivePrivateKeyForPath(masterPriv, ch, hdPath)
	if err != nil {
		return nil, "", err
	}
	privateKey, err := crypto.ToECDSA(privKey)
	if err != nil {
		return nil, "", err
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey).Hex()
	return privKey, address, nil
}

// savePrivateKeyFile creates private key file of the default validator.
func (b BSC) savePrivateKeyFile() error {
	fpath := filepath.Join(b.config.HomeDir, privateKeyFileName)
	encodedKey := make([]byte, len(b.config.FaucetPrivateKey)*2)
	hex.Encode(encodedKey, b.config.FaucetPrivateKey)
	return os.WriteFile(fpath, encodedKey, 0o600)
}

// saveRunScriptFile renders `run.tmpl` and writes it as an executable entrypoint.
func (b BSC) saveRunScriptFile() error {
	scriptArgs := struct {
		HomePath       string
		ConfigFile     string
		PasswordFile   string
		PrivateKeyFile string
		RPCPort        int
		WSPort         int
		Validator      string
	}{
		HomePath:       targets.AppHomeDir,
		ConfigFile:     genesisFileName,
		PasswordFile:   passwordFileName,
		PrivateKeyFile: privateKeyFileName,
		RPCPort:        b.config.RPCPort,
		WSPort:         b.config.WSPort,
		Validator:      strings.ToLower(b.config.FaucetAddr),
	}

	buf := &bytes.Buffer{}
	if err := runScriptTemplate.Execute(buf, scriptArgs); err != nil {
		return errors.WithStack(err)
	}
	fpath := path.Join(b.config.HomeDir, dockerEntrypoint)
	return os.WriteFile(fpath, buf.Bytes(), 0o777) // executable
}
