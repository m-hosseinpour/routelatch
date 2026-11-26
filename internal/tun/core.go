package tun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
	_ "github.com/xtls/xray-core/proxy/tun"

	"routelatch/internal/contract"
)

// xrayCoreFactory builds the pinned core from the generated document. It
// exposes no config input: the only variable is the engine's loopback SOCKS
// target from the root-owned service config.
type xrayCoreFactory struct{}

// NewXrayCoreFactory returns the production core factory.
func NewXrayCoreFactory() CoreFactory { return xrayCoreFactory{} }

func (xrayCoreFactory) New(socksAddress string) (Core, error) {
	if !hasCapNetAdmin() {
		return nil, &contract.TunError{
			Kind:   contract.Unsupported,
			Reason: contract.TunReasonPlatformUnsupported,
		}
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return nil, &contract.TunError{
			Kind:   contract.Unsupported,
			Reason: contract.TunReasonPlatformUnsupported,
			Err:    err,
		}
	}
	raw, err := buildXrayConfig(socksAddress)
	if err != nil {
		return nil, &contract.TunError{Kind: contract.Internal, Err: err}
	}
	parsed, err := serial.LoadJSONConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, &contract.TunError{Kind: contract.Internal, Err: err}
	}
	inst, err := core.New(parsed)
	if err != nil {
		return nil, &contract.TunError{Kind: contract.Internal, Err: err}
	}
	return &xrayCore{inst: inst}, nil
}

type xrayCore struct {
	inst *core.Instance
}

func (c *xrayCore) Start(context.Context) error {
	if c.inst == nil {
		return fmt.Errorf("the TUN core is not built")
	}
	if err := c.inst.Start(); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return &contract.TunError{Kind: contract.Unsupported, Reason: contract.TunReasonPlatformUnsupported, Err: err}
		}
		return &contract.TunError{Kind: contract.Internal, Err: err}
	}
	return nil
}

// Stop closes the fresh core and lets the pinned implementation remove its
// routes, addresses and link. The service still verifies the result afterwards.
func (c *xrayCore) Stop(context.Context) error {
	if c.inst == nil {
		return nil
	}
	err := c.inst.Close()
	c.inst = nil
	if err != nil {
		return &contract.TunError{Kind: contract.Internal, Err: err}
	}
	return nil
}

func hasCapNetAdmin() bool {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "CapEff:" {
			continue
		}
		eff, err := strconv.ParseUint(fields[1], 16, 64)
		if err != nil {
			return false
		}
		return eff&(1<<12) != 0
	}
	return false
}
