package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/pkg/errors"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/bsc"

	"github.com/tokenize-x/tx-chain/v7/pkg/config/constant"
	"github.com/tokenize-x/tx-crust/znet/infra"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/bigdipper"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/blockexplorer"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/bridgexrpl"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/callisto"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/faucet"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/gaiad"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/grafana"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/hasura"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/hermes"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/osmosis"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/postgres"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/prometheus"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/txd"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/xrpl"
	"github.com/tokenize-x/tx-crust/znet/infra/cosmoschain"
)

// Factory produces apps from config.
type Factory struct {
	config infra.Config
	spec   *infra.Spec
}

// NewFactory creates new app factory.
func NewFactory(config infra.Config, spec *infra.Spec) *Factory {
	return &Factory{
		config: config,
		spec:   spec,
	}
}

// TXdNetwork creates new network of txd nodes.
//
//nolint:funlen // breaking down this function will make it less readable.
func (f *Factory) TXdNetwork(
	ctx context.Context,
	namePrefix string,
	firstPorts txd.Ports,
	validatorCount, sentryCount, seedCount, fullCount int,
	binaryVersion string,
	genDEX bool,
) (txd.TXd, []txd.TXd, error) {
	config := sdk.GetConfig()
	addressPrefix := constant.AddressPrefixDev

	// Set address & public key prefixes
	config.SetBech32PrefixForAccount(addressPrefix, addressPrefix+"pub")
	config.SetBech32PrefixForValidator(addressPrefix+"valoper", addressPrefix+"valoperpub")
	config.SetBech32PrefixForConsensusNode(addressPrefix+"valcons", addressPrefix+"valconspub")
	config.SetCoinType(constant.CoinType)

	// Prepare module balances for PSE clearing accounts
	// Using the same allocation as defined in app/upgrade/v6/pse_init.go
	// Total: 100 billion tokens (100_000_000_000_000_000 base units)
	totalMint := sdkmath.NewInt(100_000_000_000_000_000)

	moduleBalances := []txd.ModuleBalance{
		{
			ModuleName: "pse_community",
			Coins:      sdk.NewCoins(sdk.NewCoin(constant.DenomDev, totalMint.MulRaw(40).QuoRaw(100))), // 40%
		},
		{
			ModuleName: "pse_foundation",
			Coins:      sdk.NewCoins(sdk.NewCoin(constant.DenomDev, totalMint.MulRaw(30).QuoRaw(100))), // 30%
		},
		{
			ModuleName: "pse_alliance",
			Coins:      sdk.NewCoins(sdk.NewCoin(constant.DenomDev, totalMint.MulRaw(20).QuoRaw(100))), // 20%
		},
		{
			ModuleName: "pse_partnership",
			Coins:      sdk.NewCoins(sdk.NewCoin(constant.DenomDev, totalMint.MulRaw(3).QuoRaw(100))), // 3%
		},
		{
			ModuleName: "pse_investors",
			Coins:      sdk.NewCoins(sdk.NewCoin(constant.DenomDev, totalMint.MulRaw(5).QuoRaw(100))), // 5%
		},
		{
			ModuleName: "pse_team",
			Coins:      sdk.NewCoins(sdk.NewCoin(constant.DenomDev, totalMint.MulRaw(2).QuoRaw(100))), // 2%
		},
	}

	genesisConfig := txd.GenesisInitConfig{
		ChainID:       constant.ChainIDDev,
		Denom:         constant.DenomDev,
		DisplayDenom:  constant.DenomDevDisplay,
		AddressPrefix: addressPrefix,
		GenesisTime:   time.Now(),
		// These values are hardcoded in TestExpeditedGovProposalWithDepositAndWeightedVotes test of TX.
		// Remember to update that test if these values are changed
		GovConfig: txd.GovConfig{
			MinDeposit:            sdk.NewCoins(sdk.NewInt64Coin(constant.DenomDev, 1000)),
			ExpeditedMinDeposit:   sdk.NewCoins(sdk.NewInt64Coin(constant.DenomDev, 2000)),
			VotingPeriod:          20 * time.Second,
			ExpeditedVotingPeriod: 15 * time.Second,
		},
		CustomParamsConfig: txd.CustomParamsConfig{
			MinSelfDelegation: sdkmath.NewInt(10_000_000),
		},
		BankBalances: []banktypes.Balance{
			// Faucet's account
			{
				Address: txd.FaucetAddress,
				Coins:   sdk.NewCoins(sdk.NewCoin(constant.DenomDev, sdkmath.NewInt(100_000_000_000_000))),
			},
		},
		ModuleBalances: moduleBalances,
		GenTxs:         make([]json.RawMessage, 0),
	}
	// optionally enable DEX generation
	if genDEX {
		var err error
		genesisConfig, err = txd.AddDEXGenesisConfig(ctx, genesisConfig)
		if err != nil {
			return txd.TXd{}, nil, err
		}
	}

	wallet, genesisConfig := txd.NewFundedWallet(genesisConfig)

	if validatorCount > wallet.GetStakersMnemonicsCount() {
		return txd.TXd{}, nil, errors.Errorf(
			"unsupported validators count: %d, max: %d",
			validatorCount,
			wallet.GetStakersMnemonicsCount(),
		)
	}

	nodes := make([]txd.TXd, 0, validatorCount+seedCount+sentryCount+fullCount)
	valNodes := make([]txd.TXd, 0, validatorCount)
	seedNodes := make([]txd.TXd, 0, seedCount)
	var lastNode txd.TXd
	var name string
	for i := range cap(nodes) {
		portDelta := i * 100
		isValidator := i < validatorCount
		isSeed := !isValidator && i < validatorCount+seedCount
		isSentry := !isValidator && !isSeed && i < validatorCount+seedCount+sentryCount
		isFull := !isValidator && !isSeed && !isSentry

		name = namePrefix + fmt.Sprintf("-%02d", i)
		dockerImage := txd.DockerImageStandard
		switch {
		case isValidator:
			name += "-val"
		case isSentry:
			name += "-sentry"
		case isSeed:
			name += "-seed"
		default:
			name += "-full"
		}

		node := txd.New(txd.Config{
			Name:              name,
			HomeDir:           filepath.Join(f.config.AppDir, name, string(genesisConfig.ChainID)),
			BinDir:            filepath.Join(f.config.RootDir, "bin"),
			WrapperDir:        f.config.WrapperDir,
			DockerImage:       dockerImage,
			GenesisInitConfig: &genesisConfig,
			AppInfo:           f.spec.DescribeApp(txd.AppType, name),
			Ports: txd.Ports{
				RPC:        firstPorts.RPC + portDelta,
				P2P:        firstPorts.P2P + portDelta,
				GRPC:       firstPorts.GRPC + portDelta,
				GRPCWeb:    firstPorts.GRPCWeb + portDelta,
				API:        firstPorts.API + portDelta,
				PProf:      firstPorts.PProf + portDelta,
				Prometheus: firstPorts.Prometheus + portDelta,
			},
			IsValidator: isValidator,
			StakerMnemonic: func() string {
				if isValidator {
					return wallet.GetStakersMnemonic(i)
				}
				return ""
			}(),
			StakerBalance: wallet.GetStakerMnemonicsBalance(),
			ValidatorNodes: func() []txd.TXd {
				if isSentry || sentryCount == 0 {
					return valNodes
				}

				return nil
			}(),
			SeedNodes: func() []txd.TXd {
				if isSentry || isFull {
					return seedNodes
				}

				return nil
			}(),
			ImportedMnemonics: map[string]string{
				"alice":      txd.AliceMnemonic,
				"bob":        txd.BobMnemonic,
				"charlie":    txd.CharlieMnemonic,
				"xrplbridge": bridgexrpl.TXChainAdminMnemonic,
			},
			FundingMnemonic: txd.FundingMnemonic,
			FaucetMnemonic:  txd.FaucetMnemonic,
			GasPriceStr:     txd.DefaultGasPriceStr,
			BinaryVersion:   binaryVersion,
			TimeoutCommit:   f.spec.TimeoutCommit,
			Upgrades:        f.config.TXdUpgrades,
		})
		if isValidator {
			valNodes = append(valNodes, node)
		}
		if isSeed {
			seedNodes = append(seedNodes, node)
		}
		lastNode = node
		nodes = append(nodes, node)
	}
	return lastNode, nodes, nil
}

// Faucet creates new faucet.
func (f *Factory) Faucet(name string, txdApp txd.TXd) faucet.Faucet {
	return faucet.New(faucet.Config{
		Name:           name,
		HomeDir:        filepath.Join(f.config.AppDir, name),
		AppInfo:        f.spec.DescribeApp(faucet.AppType, name),
		Port:           faucet.DefaultPort,
		MonitoringPort: faucet.DefaultMonitoringPort,
		TXd:            txdApp,
	})
}

// BlockExplorer returns set of applications required to run block explorer.
func (f *Factory) BlockExplorer(prefix string, txdApp txd.TXd) blockexplorer.Explorer {
	namePostgres := BuildPrefixedAppName(prefix, string(postgres.AppType))
	nameHasura := BuildPrefixedAppName(prefix, string(hasura.AppType))
	nameCallisto := BuildPrefixedAppName(prefix, string(callisto.AppType))
	nameBigDipper := BuildPrefixedAppName(prefix, string(bigdipper.AppType))

	postgresApp := postgres.New(postgres.Config{
		Name:    namePostgres,
		AppInfo: f.spec.DescribeApp(postgres.AppType, namePostgres),
		Port:    blockexplorer.DefaultPorts.Postgres,
	})
	callistoApp := callisto.New(callisto.Config{
		Name:            nameCallisto,
		HomeDir:         filepath.Join(f.config.AppDir, nameCallisto),
		RepoDir:         filepath.Clean(filepath.Join(f.config.RootDir, "../callisto")),
		AppInfo:         f.spec.DescribeApp(callisto.AppType, nameCallisto),
		Port:            blockexplorer.DefaultPorts.Callisto,
		TelemetryPort:   blockexplorer.DefaultPorts.CallistoTelemetry,
		ConfigTemplate:  blockexplorer.CallistoConfigTemplate,
		TXd:             txdApp,
		Postgres:        postgresApp,
		ContractAddress: blockexplorer.DefaultContractAddress,
	})
	hasuraApp := hasura.New(hasura.Config{
		Name:     nameHasura,
		AppInfo:  f.spec.DescribeApp(hasura.AppType, nameHasura),
		Port:     blockexplorer.DefaultPorts.Hasura,
		Postgres: postgresApp,
		Callisto: callistoApp,
	})
	bigDipperApp := bigdipper.New(bigdipper.Config{
		Name:    nameBigDipper,
		AppInfo: f.spec.DescribeApp(bigdipper.AppType, nameBigDipper),
		Port:    blockexplorer.DefaultPorts.BigDipper,
		TXd:     txdApp,
		Hasura:  hasuraApp,
	})

	return blockexplorer.Explorer{
		Postgres:  postgresApp,
		Callisto:  callistoApp,
		Hasura:    hasuraApp,
		BigDipper: bigDipperApp,
	}
}

// IBC creates set of applications required to test IBC.
func (f *Factory) IBC(prefix string, txdApp txd.TXd) infra.AppSet {
	nameGaia := BuildPrefixedAppName(prefix, string(gaiad.AppType))
	nameOsmosis := BuildPrefixedAppName(prefix, string(osmosis.AppType))
	nameRelayerHermes := BuildPrefixedAppName(prefix, string(hermes.AppType))

	gaiaApp := gaiad.New(cosmoschain.AppConfig{
		Name:              nameGaia,
		HomeDir:           filepath.Join(f.config.AppDir, nameGaia),
		ChainID:           gaiad.DefaultChainID,
		HomeName:          gaiad.DefaultHomeName,
		AppInfo:           f.spec.DescribeApp(gaiad.AppType, nameGaia),
		Ports:             gaiad.DefaultPorts,
		RelayerMnemonic:   gaiad.RelayerMnemonic,
		FundingMnemonic:   gaiad.FundingMnemonic,
		TimeoutCommit:     f.config.TimeoutCommit,
		WrapperDir:        f.config.WrapperDir,
		GasPriceStr:       gaiad.DefaultGasPriceStr,
		RunScriptTemplate: gaiad.RunScriptTemplate,
	})

	osmosisApp := osmosis.New(cosmoschain.AppConfig{
		Name:              nameOsmosis,
		HomeDir:           filepath.Join(f.config.AppDir, nameOsmosis),
		ChainID:           osmosis.DefaultChainID,
		HomeName:          osmosis.DefaultHomeName,
		AppInfo:           f.spec.DescribeApp(osmosis.AppType, nameOsmosis),
		Ports:             osmosis.DefaultPorts,
		RelayerMnemonic:   osmosis.RelayerMnemonic,
		FundingMnemonic:   osmosis.FundingMnemonic,
		TimeoutCommit:     f.config.TimeoutCommit,
		WrapperDir:        f.config.WrapperDir,
		GasPriceStr:       osmosis.DefaultGasPriceStr,
		RunScriptTemplate: osmosis.RunScriptTemplate,
	})

	hermesApp := hermes.New(hermes.Config{
		Name:                   nameRelayerHermes,
		HomeDir:                filepath.Join(f.config.AppDir, nameRelayerHermes),
		AppInfo:                f.spec.DescribeApp(hermes.AppType, nameRelayerHermes),
		TelemetryPort:          hermes.DefaultTelemetryPort,
		TXd:                    txdApp,
		TXChainRelayerMnemonic: txd.RelayerMnemonic,
		PeeredChains:           []cosmoschain.BaseApp{gaiaApp, osmosisApp},
	})

	return infra.AppSet{
		gaiaApp,
		osmosisApp,
		hermesApp,
	}
}

// Monitoring returns set of applications required to run monitoring.
func (f *Factory) Monitoring(
	prefix string,
	txdNodes []txd.TXd,
	faucet faucet.Faucet,
	callisto callisto.Callisto,
	hermesApps []hermes.Hermes,
) infra.AppSet {
	namePrometheus := BuildPrefixedAppName(prefix, string(prometheus.AppType))
	nameGrafana := BuildPrefixedAppName(prefix, string(grafana.AppType))

	prometheusApp := prometheus.New(prometheus.Config{
		Name:       namePrometheus,
		HomeDir:    filepath.Join(f.config.AppDir, namePrometheus),
		Port:       prometheus.DefaultPort,
		AppInfo:    f.spec.DescribeApp(prometheus.AppType, namePrometheus),
		TXdNodes:   txdNodes,
		Faucet:     faucet,
		Callisto:   callisto,
		HermesApps: hermesApps,
	})

	grafanaApp := grafana.New(grafana.Config{
		Name:       nameGrafana,
		HomeDir:    filepath.Join(f.config.AppDir, nameGrafana),
		AppInfo:    f.spec.DescribeApp(grafana.AppType, nameGrafana),
		TXdNodes:   txdNodes,
		Port:       grafana.DefaultPort,
		Prometheus: prometheusApp,
	})

	return infra.AppSet{
		prometheusApp,
		grafanaApp,
	}
}

// XRPL returns xrpl node app set.
func (f *Factory) XRPL(prefix string) xrpl.XRPL {
	nameXRPL := BuildPrefixedAppName(prefix, string(xrpl.AppType))

	return xrpl.New(xrpl.Config{
		Name:       nameXRPL,
		HomeDir:    filepath.Join(f.config.AppDir, nameXRPL),
		AppInfo:    f.spec.DescribeApp(xrpl.AppType, nameXRPL),
		RPCPort:    xrpl.DefaultRPCPort,
		WSPort:     xrpl.DefaultWSPort,
		FaucetSeed: xrpl.DefaultFaucetSeed,
	})
}

// BridgeXRPLRelayers returns a set of XRPL relayer apps.
func (f *Factory) BridgeXRPLRelayers(
	prefix string,
	txdApp txd.TXd,
	xrplApp xrpl.XRPL,
	relayerCount int,
) (infra.AppSet, error) {
	if relayerCount > len(bridgexrpl.RelayerMnemonics) {
		return nil, errors.Errorf(
			"unsupported relayer count: %d, max: %d",
			relayerCount,
			len(bridgexrpl.RelayerMnemonics),
		)
	}

	var leader *bridgexrpl.Bridge
	relayers := make(infra.AppSet, 0, relayerCount)
	ports := bridgexrpl.DefaultPorts
	for i := range relayerCount {
		name := fmt.Sprintf("%s-%02d", BuildPrefixedAppName(prefix, string(bridgexrpl.AppType)), i)
		relayer := bridgexrpl.New(bridgexrpl.Config{
			Name:    name,
			HomeDir: filepath.Join(f.config.AppDir, name),
			ContractPath: filepath.Clean(filepath.Join(f.config.RootDir, "../tx-xrpl-bridge", "contract", "artifacts",
				"tx_xrpl_bridge.wasm")),
			Mnemonics: bridgexrpl.RelayerMnemonics[i],
			Quorum:    uint32(relayerCount),
			AppInfo:   f.spec.DescribeApp(bridgexrpl.AppType, name),
			Ports:     ports,
			Leader:    leader,
			TXd:       txdApp,
			XRPL:      xrplApp,
		})
		ports.Metrics++
		if leader == nil {
			leader = &relayer
		}

		relayers = append(relayers, relayer)
	}

	return relayers, nil
}

// BSC returns binance smart chain node app set.
func (f *Factory) BSC(prefix string) bsc.BSC {
	nameBSC := BuildPrefixedAppName(prefix, string(bsc.AppType))

	faucetPrivateKey, faucetAddr, err := bsc.ExtractKeyPairsFromSeed(bsc.FundingMnemonic)
	if err != nil {
		panic(err)
	}

	return bsc.New(bsc.Config{
		Name:             nameBSC,
		HomeDir:          filepath.Join(f.config.AppDir, nameBSC),
		AppInfo:          f.spec.DescribeApp(bsc.AppType, nameBSC),
		RPCPort:          bsc.DefaultRPCPort,
		WSPort:           bsc.DefaultWSPort,
		ChainID:          1337, // private‑net chain ID
		FaucetAddr:       faucetAddr,
		FaucetPrivateKey: faucetPrivateKey,
	})
}

// BuildPrefixedAppName builds the app name based on its prefix and name.
func BuildPrefixedAppName(prefix string, names ...string) string {
	return strings.Join(append([]string{prefix}, names...), "-")
}
