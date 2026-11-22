package hscontrol

import (
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"tailscale.com/net/tsaddr"
)

// Private networks: subnet routers (devices advertising routes into a VPC
// or office network) and named private resources behind them, such as an
// RDS database, usable in access rules and temporary access.

func (aa *adminAuth) networkRoutes(r chi.Router) {
	r.Get("/networks", aa.handleListNetworks)
	r.Post("/resources", aa.handleSaveResource)
	r.Put("/resources/{id}", aa.handleSaveResource)
	r.Delete("/resources/{id}", aa.handleDeleteResource)
}

type adminRouter struct {
	ID        uint64   `json:"id"`
	Name      string   `json:"name"`
	Online    bool     `json:"online"`
	Tags      []string `json:"tags"`
	Announced []string `json:"announced"`
	Approved  []string `json:"approved"`
	Pending   []string `json:"pending"`
}

type adminResource struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	// CoveredBy names the routers with an approved route covering the
	// address. Empty means nothing forwards traffic to it yet.
	CoveredBy []string `json:"coveredBy"`
}

// subnetRoute reports whether p is a route into a private network: not an
// exit node's default route.
func subnetRoute(p netip.Prefix) bool { return p.Bits() > 0 }

func (aa *adminAuth) handleListNetworks(w http.ResponseWriter, _ *http.Request) {
	routers := []adminRouter{}

	type approved struct {
		router string
		prefix netip.Prefix
	}

	var covering []approved

	for _, n := range aa.h.state.ListNodes().All() {
		rt := adminRouter{ID: uint64(n.ID()), Name: n.GivenName(), Tags: n.Tags().AsSlice(), Announced: []string{}, Approved: []string{}, Pending: []string{}}
		if o := n.IsOnline(); o.Valid() {
			rt.Online = o.Get()
		}

		approvedSet := map[netip.Prefix]bool{}

		for _, p := range n.ApprovedRoutes().All() {
			if !subnetRoute(p) {
				continue
			}

			approvedSet[p] = true
			rt.Approved = append(rt.Approved, p.String())
			covering = append(covering, approved{router: rt.Name, prefix: p})
		}

		for _, p := range n.AnnouncedRoutes() {
			if !subnetRoute(p) {
				continue
			}

			rt.Announced = append(rt.Announced, p.String())
			if !approvedSet[p] {
				rt.Pending = append(rt.Pending, p.String())
			}
		}

		if len(rt.Announced) > 0 || len(rt.Approved) > 0 {
			routers = append(routers, rt)
		}
	}

	rows, err := aa.h.state.ListNetworkResources()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	resources := make([]adminResource, 0, len(rows))

	for _, r := range rows {
		v := adminResource{ID: r.ID, Name: r.Name, Address: r.Address, Description: r.Description, CreatedAt: r.CreatedAt, CoveredBy: []string{}}

		p, err := netip.ParsePrefix(r.Address)
		if err == nil {
			for _, c := range covering {
				if c.prefix.Bits() <= p.Bits() && c.prefix.Contains(p.Addr()) && !slices.Contains(v.CoveredBy, c.router) {
					v.CoveredBy = append(v.CoveredBy, c.router)
				}
			}
		}

		resources = append(resources, v)
	}

	adminJSON(w, http.StatusOK, map[string]any{"routers": routers, "resources": resources})
}

// parseResourceAddress accepts an IP address or a range and returns it as
// a masked prefix. Ranges inside the tailnet, and everything at once, are
// refused: those are devices and the internet, not private resources.
func parseResourceAddress(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)

	var p netip.Prefix

	a, err := netip.ParseAddr(s)
	if err == nil {
		p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
	} else {
		p, err = netip.ParsePrefix(s)
		if err != nil {
			return p, userError("enter an IP address such as 10.0.1.25, or a range such as 10.0.1.0/24")
		}

		p = p.Masked()
	}

	if p.Bits() == 0 {
		return p, userError("a private resource cannot be the whole internet")
	}

	if tsaddr.CGNATRange().Overlaps(p) || tsaddr.TailscaleULARange().Overlaps(p) {
		return p, userError("that range belongs to devices on this network; give access to the devices instead")
	}

	return p, nil
}

func (aa *adminAuth) handleSaveResource(w http.ResponseWriter, r *http.Request) {
	var id uint64

	if s := chi.URLParam(r, "id"); s != "" {
		var err error

		id, err = strconv.ParseUint(s, 10, 64)
		if err != nil {
			adminJSONError(w, http.StatusBadRequest, "invalid id")

			return
		}
	}

	var body struct {
		Name        string `json:"name"`
		Address     string `json:"address"`
		Description string `json:"description"`
	}

	if !decodeJSON(w, r, &body) {
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 100 {
		adminJSONError(w, http.StatusBadRequest, "give the resource a name, e.g. orders database")

		return
	}

	p, err := parseResourceAddress(body.Address)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	newID, changes, err := aa.h.state.SaveNetworkResource(types.NetworkResource{
		ID: id, Name: name, Address: p.String(), Description: strings.TrimSpace(truncateString(body.Description, 300)),
		CreatedBy: sessionFrom(r).label(),
	})
	aa.h.Change(changes...)

	if errors.Is(err, hsdb.ErrNetworkResourceNotFound) {
		adminJSONError(w, http.StatusNotFound, "no such resource")

		return
	} else if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	aa.auditReq(r, "resource.save", name, p.String())
	adminJSON(w, http.StatusOK, map[string]uint64{"id": newID})
}

func (aa *adminAuth) handleDeleteResource(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid id")

		return
	}

	label, _ := aa.principalLabeler()

	changes, err := aa.h.state.DeleteNetworkResource(id)
	aa.h.Change(changes...)

	if errors.Is(err, hsdb.ErrNetworkResourceNotFound) {
		adminJSONError(w, http.StatusNotFound, "no such resource")

		return
	} else if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	name := strconv.FormatUint(id, 10)
	if label != nil {
		name = label(types.PrincipalResource, name)
	}

	aa.auditReq(r, "resource.delete", name, "")
	w.WriteHeader(http.StatusNoContent)
}
