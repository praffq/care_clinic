package clinic

import (
	"context"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/trust"
)

// caddyRootPEM fetches the clinic's root CA from Caddy's /root.crt over the
// service's forwarded port 80 on this computer, or "" if it can't.
func (e *Clinic) caddyRootPEM() string {
	ctx, cancel := context.WithTimeout(e.context(), 15*time.Second)
	defer cancel()
	pem, err := trust.FetchClientCertificate(ctx, e.host(), "127.0.0.1")
	if err != nil {
		e.logln("Couldn't read the clinic's certificate: " + err.Error())
		return ""
	}
	return pem
}
