package apiv2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/manoharkakumani/orgscale/hscontrol/types/change"
	"github.com/manoharkakumani/orgscale/hscontrol/util/zlog/zf"
	"github.com/rs/zerolog/log"
	"tailscale.com/tailcfg"
)

// c2nTimeout bounds how long a c2n request waits for the node to answer.
// For an update, the node replies once the update has started, not finished.
// It stays well under the server's write timeout, so the caller gets the 504.
const c2nTimeout = 10 * time.Second

// sendC2N sends a POST to path on the node's c2n handler, carried in a
// [tailcfg.PingRequest], and waits for the node's HTTP response.
func sendC2N(ctx context.Context, b Backend, nodeID types.NodeID, path string) (*http.Response, error) {
	return sendC2NMethod(ctx, b, nodeID, http.MethodPost, path)
}

// PostureIdentity asks a connected node for its serial numbers. The node
// answers with none unless posture checking is on in its settings
// (tailscale set --posture-checking, or the PostureChecking MDM key).
func PostureIdentity(ctx context.Context, b Backend, nodeID types.NodeID) ([]string, error) {
	res, err := sendC2NMethod(ctx, b, nodeID, http.MethodGet, "/posture/identity")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var out tailcfg.C2NPostureIdentityResponse

	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out)
	if err != nil {
		return nil, fmt.Errorf("reading posture identity: %w", err)
	}

	return out.SerialNumbers, nil
}

func sendC2NMethod(ctx context.Context, b Backend, nodeID types.NodeID, method, path string) (*http.Response, error) {
	c2nReq, err := http.NewRequestWithContext(ctx, method, "http://orgscale"+path, nil)
	if err != nil {
		return nil, huma.Error500InternalServerError("building c2n request", err)
	}

	var payload bytes.Buffer

	err = c2nReq.Write(&payload)
	if err != nil {
		return nil, huma.Error500InternalServerError("encoding c2n request", err)
	}

	// The client only answers c2n over Noise, and only an https URL makes
	// its Noise transport dial; the host is not used.
	callback, err := url.Parse(b.Cfg.ServerURL)
	if err != nil {
		return nil, huma.Error500InternalServerError("parsing server_url", err)
	}

	c2nID, responseCh := b.State.RegisterC2N(nodeID)
	defer b.State.CancelC2N(c2nID)

	callback.Scheme = "https"
	callback.Path = "/machine/c2n-response"
	callback.RawQuery = url.Values{"id": {c2nID}}.Encode()

	b.Change(change.PingNode(nodeID, &tailcfg.PingRequest{
		URL:     callback.String(),
		Log:     true,
		Types:   "c2n",
		Payload: payload.Bytes(),
	}))

	timer := time.NewTimer(c2nTimeout)
	defer timer.Stop()

	select {
	case raw, ok := <-responseCh:
		if !ok {
			return nil, huma.Error503ServiceUnavailable("server is shutting down")
		}

		res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), c2nReq)
		if err != nil {
			return nil, huma.Error502BadGateway("reading c2n response", err)
		}

		if res.StatusCode != http.StatusOK {
			res.Body.Close()

			// The status text is the node's; it is logged, not echoed.
			log.Info().Uint64(zf.NodeID, nodeID.Uint64()).Str("status", res.Status).Msg("node answered c2n request with an error")

			return nil, huma.Error502BadGateway("device answered c2n request with an error")
		}

		return res, nil
	case <-timer.C:
		return nil, huma.Error504GatewayTimeout("device did not answer within " + c2nTimeout.String())
	case <-ctx.Done():
		return nil, huma.Error504GatewayTimeout("request ended before the device answered", ctx.Err())
	}
}
