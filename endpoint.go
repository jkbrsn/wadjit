package wadjit

import (
	"net/url"

	"github.com/rs/xid"
)

type Endpoint struct {
	ID  xid.ID
	URL url.URL
}
