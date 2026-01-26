package apps

import (
	"context"

	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/binance"

	"github.com/tokenize-x/tx-crust/znet/infra"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/callisto"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/faucet"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/gaiad"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/hermes"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/osmosis"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/txd"
	"github.com/tokenize-x/tx-crust/znet/infra/apps/xrpl"
)

// AppPrefix constants are the prefixes used in the app factories.
const (
	AppPrefixTXd        = "txd"
	AppPrefixIBC        = "ibc"
	AppPrefixExplorer   = "explorer"
	AppPrefixMonitoring = "monitoring"
	AppPrefixXRPL       = "xrpl"
	AppPrefixBridgeXRPL = "bridge-xrpl"
	AppPrefixBinance    = "binance"
)

// Predefined Profiles.
const (
	Profile1TXd       = "1txd"
	Profile3TXd       = "3txd"
	Profile5TXd       = "5txd"
	ProfileDevNet     = "devnet"
	ProfileIBC        = "ibc"
	ProfileFaucet     = "faucet"
	ProfileExplorer   = "explorer"
	ProfileMonitoring = "monitoring"
	ProfileXRPL       = "xrpl"
	ProfileXRPLBridge = "bridge-xrpl"
	ProfileDEX        = "dex"
	ProfileBinance    = "binance"
)

var profiles = []string{
	Profile1TXd,
	Profile3TXd,
	Profile5TXd,
	ProfileDevNet,
	ProfileIBC,
	ProfileFaucet,
	ProfileExplorer,
	ProfileMonitoring,
	ProfileXRPL,
	ProfileXRPLBridge,
	ProfileDEX,
	ProfileBinance,
}

var defaultProfiles = []string{Profile1TXd}

var availableProfiles = func() map[string]struct{} {
	v := map[string]struct{}{}
	for _, p := range profiles {
		v[p] = struct{}{}
	}
	return v
}()

// Profiles returns the list of available profiles.
func Profiles() []string {
	return profiles
}

// DefaultProfiles returns the list of default profiles started if user didn't provide anything else.
func DefaultProfiles() []string {
	return defaultProfiles
}

// ValidateProfiles verifies that profile set is correct.
func ValidateProfiles(profiles []string) error {
	pMap := map[string]bool{}
	txdProfilePresent := false
	for _, p := range profiles {
		if _, ok := availableProfiles[p]; !ok {
			return errors.Errorf("profile %s does not exist", p)
		}
		if p == Profile1TXd || p == Profile3TXd || p == Profile5TXd || p == ProfileDevNet {
			if txdProfilePresent {
				return errors.Errorf("profiles 1txd, 3txd, 5txd and devnet are mutually exclusive")
			}
			txdProfilePresent = true
		}
		pMap[p] = true
	}

	return nil
}

// MergeProfiles removes redundant profiles from the list.
func MergeProfiles(pMap map[string]bool) map[string]bool {
	switch {
	case pMap[ProfileDevNet]:
		delete(pMap, Profile1TXd)
		delete(pMap, Profile3TXd)
		delete(pMap, Profile5TXd)
	case pMap[Profile5TXd]:
		delete(pMap, Profile1TXd)
		delete(pMap, Profile3TXd)
	case pMap[Profile3TXd]:
		delete(pMap, Profile1TXd)
	}

	return pMap
}

// BuildAppSet builds the application set to deploy based on provided profiles.
//
//nolint:funlen
func BuildAppSet(ctx context.Context, appF *Factory, profiles []string, txdVersion string) (
	infra.AppSet, txd.TXd, error,
) {
	pMap := lo.SliceToMap(profiles, func(profile string) (string, bool) {
		return profile, true
	})

	if pMap[ProfileIBC] || pMap[ProfileFaucet] || pMap[ProfileXRPLBridge] ||
		pMap[ProfileExplorer] || pMap[ProfileMonitoring] {
		pMap[Profile1TXd] = true
	}

	if pMap[ProfileXRPLBridge] {
		pMap[ProfileXRPL] = true
	}

	MergeProfiles(pMap)

	validatorCount, sentryCount, seedCount, fullCount := decideNumOfTXdNodes(pMap)

	var txdApp txd.TXd
	var appSet infra.AppSet

	var genDEX bool
	if pMap[ProfileDEX] {
		genDEX = true
	}

	txdApp, txdNodes, err := appF.TXdNetwork(
		ctx,
		AppPrefixTXd,
		txd.DefaultPorts,
		validatorCount, sentryCount, seedCount, fullCount,
		txdVersion, genDEX,
	)
	if err != nil {
		return nil, txd.TXd{}, err
	}
	for _, txdNode := range txdNodes {
		appSet = append(appSet, txdNode)
	}

	if pMap[ProfileIBC] {
		appSet = append(appSet, appF.IBC(AppPrefixIBC, txdApp)...)
	}

	var faucetApp faucet.Faucet
	if pMap[ProfileFaucet] {
		appSet = append(appSet, appF.Faucet(string(faucet.AppType), txdApp))
	}

	if pMap[ProfileExplorer] {
		appSet = append(appSet, appF.BlockExplorer(AppPrefixExplorer, txdApp).ToAppSet()...)
	}

	if pMap[ProfileMonitoring] {
		var callistoApp callisto.Callisto
		if callistoAppSetApp, ok := appSet.FindAppByName(
			BuildPrefixedAppName(AppPrefixExplorer, string(callisto.AppType)),
		).(callisto.Callisto); ok {
			callistoApp = callistoAppSetApp
		}

		var hermesApps []hermes.Hermes
		if hermesAppSetApp, ok := appSet.FindAppByName(
			BuildPrefixedAppName(AppPrefixIBC, string(hermes.AppType), string(gaiad.AppType)),
		).(hermes.Hermes); ok {
			hermesApps = append(hermesApps, hermesAppSetApp)
		}

		if hermesAppSetApp, ok := appSet.FindAppByName(
			BuildPrefixedAppName(AppPrefixIBC, string(hermes.AppType), string(osmosis.AppType)),
		).(hermes.Hermes); ok {
			hermesApps = append(hermesApps, hermesAppSetApp)
		}

		appSet = append(appSet, appF.Monitoring(
			AppPrefixMonitoring,
			txdNodes,
			faucetApp,
			callistoApp,
			hermesApps,
		)...)
	}

	var xrplApp xrpl.XRPL
	if pMap[ProfileXRPL] {
		xrplApp = appF.XRPL(AppPrefixXRPL)
		appSet = append(appSet, xrplApp)
	}

	if pMap[ProfileXRPLBridge] {
		relayers, err := appF.BridgeXRPLRelayers(
			AppPrefixBridgeXRPL,
			txdApp,
			xrplApp,
			3,
		)
		if err != nil {
			return nil, txd.TXd{}, err
		}
		appSet = append(appSet, relayers...)
	}

	if pMap[ProfileBinance] {
		var binanceApp binance.Binance
		binanceApp = appF.Binance(AppPrefixBinance)
		appSet = append(appSet, binanceApp)
	}

	return appSet, txdApp, nil
}

func decideNumOfTXdNodes(pMap map[string]bool) (validatorCount, sentryCount, seedCount, fullCount int) {
	switch {
	case pMap[Profile1TXd]:
		return 1, 0, 0, 0
	case pMap[Profile3TXd]:
		return 3, 0, 0, 0
	case pMap[Profile5TXd]:
		return 5, 0, 0, 0
	case pMap[ProfileDevNet]:
		return 3, 1, 1, 2
	default:
		panic("no txd profile specified.")
	}
}
