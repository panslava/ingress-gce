package healthchecks

import (
	"fmt"

	utilnet "k8s.io/utils/net"
)

const (
	l4ELBIPv6HCRangeString = "2600:1901:8001::/48"
	l4ILBIPv6HCRangeString = "2600:2d00:1:b029::/64"
)

var (
	L4ELBIPv6HCRange utilnet.IPNetSet
	L4ILBIPv6HCRange utilnet.IPNetSet
)

func init() {
	var err error
	L4ELBIPv6HCRange, err = utilnet.ParseIPNets([]string{l4ELBIPv6HCRangeString}...)
	if err != nil {
		panic(fmt.Sprintf("utilnet.ParseIPNets([]string{%s}...) returned error %v, want nil", l4ELBIPv6HCRangeString, err))
	}

	L4ILBIPv6HCRange, err = utilnet.ParseIPNets([]string{l4ILBIPv6HCRangeString}...)
	if err != nil {
		panic(fmt.Sprintf("utilnet.ParseIPNets([]string{%s}...) returned error %v, want nil", l4ILBIPv6HCRangeString, err))
	}
}
