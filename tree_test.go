package mmdbwriter

import (
	"bytes"
	"fmt"
	"net"
	"testing"

	"github.com/oschwald/maxminddb-golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testInsert struct {
	network string
	value   DataType
}

type testInsertError struct {
	network          string
	value            DataType
	expectedErrorMsg string
}

type testGet struct {
	ip                  string
	expectedNetwork     string
	expectedGetValue    *DataType
	expectedLookupValue *interface{}
}

func TestTreeInsertAndGet(t *testing.T) {
	tests := []struct {
		name                    string
		disableIPv4Aliasing     bool
		includeReservedNetworks bool
		inserts                 []testInsert
		insertErrors            []testInsertError
		gets                    []testGet
		expectedNodeCount       int
	}{
		{
			name:                    "::/1 insert, IPv4 lookup",
			includeReservedNetworks: true,
			inserts: []testInsert{
				{
					network: "::/1",
					value:   String("string"),
				},
			},
			gets: []testGet{
				{
					ip:                  "1.1.1.1",
					expectedNetwork:     "0.0.0.0/1",
					expectedGetValue:    s2dtp("string"),
					expectedLookupValue: s2ip("string"),
				},
			},
			expectedNodeCount: 142,
		},
		{
			name:                    "8000::/1 insert",
			includeReservedNetworks: true,
			inserts: []testInsert{
				{
					network: "8000::/1",
					value:   String("string"),
				},
			},
			gets: []testGet{
				{
					ip:                  "8000::",
					expectedNetwork:     "8000::/1",
					expectedGetValue:    s2dtp("string"),
					expectedLookupValue: s2ip("string"),
				},
			},
			expectedNodeCount: 142,
		},
		{
			name:                    "overwriting smaller network with bigger network",
			includeReservedNetworks: true,
			inserts: []testInsert{
				{
					network: "2003:1000::/32",
					value:   String("string"),
				},
				{
					network: "2003::/16",
					value:   String("new string"),
				},
			},
			gets: []testGet{
				{
					ip: "2003::",
					// Once we support pruning, this should be 2003::/16
					expectedNetwork:     "2003::/20",
					expectedGetValue:    s2dtp("new string"),
					expectedLookupValue: s2ip("new string"),
				},
				{
					ip: "2003:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
					// Once we support pruning, this should be 2003::/16
					expectedNetwork:     "2003:8000::/17",
					expectedGetValue:    s2dtp("new string"),
					expectedLookupValue: s2ip("new string"),
				},
			},
			// With pruning, this should be less
			expectedNodeCount: 158,
		},
		{
			name:                    "insert smaller network into bigger network",
			includeReservedNetworks: true,
			inserts: []testInsert{
				{
					network: "2003::/16",
					value:   String("string"),
				},
				{
					network: "2003:1000::/32",
					value:   String("new string"),
				},
			},
			gets: []testGet{
				{
					ip:                  "2003::",
					expectedNetwork:     "2003::/20",
					expectedGetValue:    s2dtp("string"),
					expectedLookupValue: s2ip("string"),
				},
				{
					ip:                  "2003:1000::",
					expectedNetwork:     "2003:1000::/32",
					expectedGetValue:    s2dtp("new string"),
					expectedLookupValue: s2ip("new string"),
				},
				{
					ip:                  "2003:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
					expectedNetwork:     "2003:8000::/17",
					expectedGetValue:    s2dtp("string"),
					expectedLookupValue: s2ip("string"),
				},
			},
			expectedNodeCount: 158,
		},
		{
			name:                    "inserting IPv4 address in IPv6 tree, without aliasing",
			disableIPv4Aliasing:     true,
			includeReservedNetworks: true,
			inserts: []testInsert{
				{
					network: "1.1.1.1/32",
					value:   String("string"),
				},
			},
			gets: []testGet{
				{
					ip:                  "1.1.1.1",
					expectedNetwork:     "1.1.1.1/32",
					expectedGetValue:    s2dtp("string"),
					expectedLookupValue: s2ip("string"),
				},
				{
					ip:                  "::1.1.1.1",
					expectedNetwork:     "::101:101/128",
					expectedGetValue:    s2dtp("string"),
					expectedLookupValue: s2ip("string"),
				},
				{
					// The IPv4 network should not be aliased
					ip:              "2002:100:100::",
					expectedNetwork: "2000::/3",
				},
			},
			expectedNodeCount: 128,
		},
		{
			name: "reserved and aliased networks",
			inserts: []testInsert{
				{
					network: "::/1",
					value:   String("string"),
				},
			},
			insertErrors: []testInsertError{
				{
					network:          "10.0.0.0/8",
					expectedErrorMsg: "attempt to insert ::a00:0/104, which is in a reserved network",
				},
				{
					network:          "10.0.0.1/32",
					expectedErrorMsg: "attempt to insert ::a00:1/128, which is in a reserved network",
				},
				{
					network:          "2002:100::/24",
					expectedErrorMsg: "attempt to insert 2002:100::/24, which is in an aliased network",
				},
			},
			gets: []testGet{
				{
					ip:                  "1.1.1.1",
					expectedNetwork:     "1.0.0.0/8",
					expectedGetValue:    s2dtp("string"),
					expectedLookupValue: s2ip("string"),
				},
				{
					// This is within a reserved network
					ip:              "203.0.113.0",
					expectedNetwork: "203.0.113.0/24",
				},
				{
					// This is in an aliased network
					ip:                  "2002:100:100::",
					expectedNetwork:     "2002:100::/24",
					expectedGetValue:    s2dtp("string"),
					expectedLookupValue: s2ip("string"),
				},
			},
			expectedNodeCount: 352,
		},
	}

	for _, recordSize := range []int{24, 28, 32} {
		t.Run(fmt.Sprintf("Record Size: %d", recordSize), func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					tree, err := New(
						Options{
							DisableIPv4Aliasing:     test.disableIPv4Aliasing,
							IncludeReservedNetworks: test.includeReservedNetworks,
							RecordSize:              recordSize,
						},
					)
					require.NoError(t, err)
					for _, insert := range test.inserts {
						_, network, err := net.ParseCIDR(insert.network)
						require.NoError(t, err)

						require.NoError(t, tree.Insert(network, insert.value))
					}

					for _, insert := range test.insertErrors {
						_, network, err := net.ParseCIDR(insert.network)
						require.NoError(t, err)

						err = tree.Insert(network, insert.value)

						assert.EqualError(t, err, insert.expectedErrorMsg)
					}

					for _, get := range test.gets {
						network, value := tree.Get(net.ParseIP(get.ip))

						assert.Equal(t, get.expectedNetwork, network.String(), "network for %s", get.ip)
						assert.Equal(t, get.expectedGetValue, value, "value for %s", get.ip)
					}

					tree.Finalize()

					assert.Equal(t, test.expectedNodeCount, tree.nodeCount)

					buf := &bytes.Buffer{}
					numBytes, err := tree.WriteTo(buf)
					require.NoError(t, err)

					reader, err := maxminddb.FromBytes(buf.Bytes())
					require.NoError(t, err)

					for _, get := range test.gets {
						var v interface{}
						network, ok, err := reader.LookupNetwork(net.ParseIP(get.ip), &v)
						require.NoError(t, err)

						assert.Equal(t, get.expectedNetwork, network.String(), "network for %s in database", get.ip)

						if get.expectedLookupValue == nil {
							assert.False(t, ok, "%s is not in the database", get.ip)
						} else {
							assert.Equal(t, *get.expectedLookupValue, v, "value for %s in database", get.ip)
						}
					}
					assert.Equal(t, int64(buf.Len()), numBytes, "number of bytes")
				})
			}
		})
	}
}

func s2ip(v string) *interface{} {
	i := interface{}(v)
	return &i
}

func s2dtp(v string) *DataType {
	ts := DataType(String(v))
	return &ts
}