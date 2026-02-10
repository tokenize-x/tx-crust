package bsc

import (
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/require"
)

func TestExtractKeyPairsFromSeed(t *testing.T) {
	priv, addr, err := ExtractKeyPairsFromSeed(FundingMnemonic)
	require.NoError(t, err)
	require.Equal(t, "0x3e89ee5411e339dfb1d9e56cb24ba9b87d8eb795b9e41fe2cd6d024870ff43d3", hexutil.Encode(priv))
	require.Equal(t, "0x1eB4F656E576989a3F544fb55Da3E7bE32c4923f", addr)
}
