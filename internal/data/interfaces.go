package data

import (
	"net/netip"
	"time"

	"github.com/qdm12/ddns-updater/internal/models"
	"github.com/qdm12/ddns-updater/pkg/publicip/ipversion"
)

type PersistentDatabase interface {
	Close() error
	StoreNewIP(domain, owner string, ip netip.Addr, t time.Time) (err error)
	GetEvents(domain, owner string, ipVersion ipversion.IPVersion) (
		events []models.HistoryEvent, err error,
	)
}
