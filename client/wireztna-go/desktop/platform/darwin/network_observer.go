//go:build darwin

package darwin

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/wireztna/client/desktop/controller"
)

const (
	defaultNetworkPollInterval = 2 * time.Second
	defaultWakeThreshold       = 8 * time.Second
)

// NetworkObserver uses a read-only underlay signature and delayed-timer wake
// detection. utun/loopback devices are excluded so WireZTNA's own reconciliation
// cannot feed an endless network-change loop.
type NetworkObserver struct {
	pollInterval time.Duration
	wakeAfter    time.Duration
	now          func() time.Time
	signature    func() (string, error)
}

func NewNetworkObserver() *NetworkObserver {
	return &NetworkObserver{
		pollInterval: defaultNetworkPollInterval,
		wakeAfter:    defaultWakeThreshold,
		now:          time.Now,
		signature:    underlaySignature,
	}
}

func (o *NetworkObserver) Events(ctx context.Context) (<-chan controller.NetworkEvent, error) {
	if ctx == nil {
		return nil, errors.New("network observer context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o == nil || o.pollInterval <= 0 || o.wakeAfter <= o.pollInterval || o.now == nil || o.signature == nil {
		return nil, errors.New("network observer configuration is invalid")
	}
	initial, err := o.signature()
	if err != nil {
		return nil, err
	}
	events := make(chan controller.NetworkEvent, 1)
	go func() {
		defer close(events)
		ticker := time.NewTicker(o.pollInterval)
		defer ticker.Stop()
		lastTick := o.now()
		lastSignature := initial
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := o.now()
				kind := controller.NetworkEventKind("")
				if now.Sub(lastTick) >= o.wakeAfter {
					kind = controller.NetworkEventWake
				}
				lastTick = now
				signature, signatureErr := o.signature()
				if signatureErr == nil && signature != lastSignature {
					lastSignature = signature
					if kind == "" {
						kind = controller.NetworkEventChanged
					}
				}
				if kind == "" {
					continue
				}
				select {
				case events <- controller.NetworkEvent{Kind: kind, OccurredAt: now.UTC()}:
				default:
				}
			}
		}
	}()
	return events, nil
}

func underlaySignature() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagLoopback != 0 || strings.HasPrefix(networkInterface.Name, "utun") {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			return "", addressErr
		}
		addressParts := make([]string, 0, len(addresses))
		for _, address := range addresses {
			addressParts = append(addressParts, address.String())
		}
		sort.Strings(addressParts)
		parts = append(parts, networkInterface.Name+"|"+networkInterface.Flags.String()+"|"+strings.Join(addressParts, ","))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";"), nil
}
