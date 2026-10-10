package pdu

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDistributionList(t *testing.T) {
	_, err := NewDistributionList("1234567890123456789012")
	require.NotNil(t, err)
}

func TestDistributionListTerminatorLimit(t *testing.T) {
	d, err := NewDistributionList(strings.Repeat("A", 20))
	require.NoError(t, err)
	require.Error(t, d.SetName(strings.Repeat("B", 21)))
	require.Equal(t, strings.Repeat("A", 20), d.Name())
	require.Error(t, d.SetName("A\x00B"))
	var decoded DistributionList
	require.NoError(t, decoded.Unmarshal(NewBuffer(append([]byte(strings.Repeat("A", 21)), 0))))
	require.Equal(t, strings.Repeat("A", 21), decoded.Name())
}
