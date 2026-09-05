package nodeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GrantTrafficPackage makes the fake an Agent that takes packages. The grant
// folds into the fake's usage the way the real store folds it.
func (h *fakeHandler) GrantTrafficPackage(_ context.Context, grant TrafficPackageGrant) (TrafficUsageUpdate, error) {
	h.packageGrant = grant
	if h.trafficErr != nil {
		return TrafficUsageUpdate{}, h.trafficErr
	}
	previous := h.trafficUsage
	h.trafficUsage.Package = previous.Package.Add(grant.Package())
	h.trafficUsage.CycleStart = grant.ExpectedCycleStart
	return TrafficUsageUpdate{Previous: previous, Applied: h.trafficUsage, Warning: h.trafficWarning}, nil
}

func TestTrafficUsageCarriesThePackageBothWays(t *testing.T) {
	const cycleStart = int64(1_782_864_000)
	h := &fakeHandler{trafficUsage: TrafficUsage{
		InBytes: 100, OutBytes: 200, CycleStart: cycleStart,
		Package: TrafficPackage{InBytes: 5, TotalBytes: 50},
	}}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()

	got, err := client.TrafficUsage(context.Background())
	if err != nil || got != h.trafficUsage {
		t.Fatalf("TrafficUsage = %+v, err=%v, want %+v", got, err, h.trafficUsage)
	}

	// A replacement that names a package replaces it.
	req := TrafficUsageRequest{
		InBytes: 300, OutBytes: 400, ExpectedCycleStart: cycleStart,
		Package: &TrafficPackage{InBytes: 7, OutBytes: 8, TotalBytes: 9},
	}
	updated, err := client.SetTrafficUsage(context.Background(), req)
	if err != nil {
		t.Fatalf("SetTrafficUsage with package: %v", err)
	}
	if h.trafficReq.Package == nil || *h.trafficReq.Package != *req.Package ||
		updated.Previous != got || updated.Applied.Package != *req.Package ||
		updated.Applied.InBytes != 300 || updated.Applied.OutBytes != 400 {
		t.Fatalf("package replacement: request=%+v response=%+v", h.trafficReq, updated)
	}

	// One that names none — what an older Hub sends — leaves it alone.
	older := TrafficUsageRequest{InBytes: 1, OutBytes: 2, ExpectedCycleStart: cycleStart}
	updated, err = client.SetTrafficUsage(context.Background(), older)
	if err != nil {
		t.Fatalf("SetTrafficUsage without package: %v", err)
	}
	if h.trafficReq.Package != nil || updated.Applied.Package != *req.Package || updated.Applied.InBytes != 1 {
		t.Fatalf("usage-only replacement: request=%+v response=%+v", h.trafficReq, updated)
	}
}

func TestGrantTrafficPackageRoundTripAndConflicts(t *testing.T) {
	const cycleStart = int64(1_782_864_000)
	h := &fakeHandler{
		trafficUsage:   TrafficUsage{InBytes: 100, OutBytes: 200, CycleStart: cycleStart, Package: TrafficPackage{InBytes: 5}},
		trafficWarning: "package granted; quota state needs inspection",
	}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()

	grant := TrafficPackageGrant{InBytes: 10, TotalBytes: 40, ExpectedCycleStart: cycleStart}
	update, err := client.GrantTrafficPackage(context.Background(), grant)
	if err != nil {
		t.Fatalf("GrantTrafficPackage: %v", err)
	}
	if h.packageGrant != grant || update.Previous.Package != (TrafficPackage{InBytes: 5}) ||
		update.Applied.Package != (TrafficPackage{InBytes: 15, TotalBytes: 40}) ||
		update.Applied.InBytes != 100 || update.Applied.OutBytes != 200 ||
		update.Warning != h.trafficWarning {
		t.Fatalf("grant: request=%+v response=%+v", h.packageGrant, update)
	}

	// An empty grant is refused before it leaves the Hub.
	h.packageGrant = TrafficPackageGrant{}
	if _, err := client.GrantTrafficPackage(context.Background(), TrafficPackageGrant{ExpectedCycleStart: cycleStart}); err == nil ||
		!strings.Contains(err.Error(), "at least one byte") {
		t.Fatalf("empty grant error = %v", err)
	}
	if h.packageGrant != (TrafficPackageGrant{}) {
		t.Fatalf("empty grant reached the Agent: %+v", h.packageGrant)
	}

	h.trafficErr = TrafficCycleConflict()
	_, err = client.GrantTrafficPackage(context.Background(), grant)
	if err == nil || !strings.Contains(err.Error(), "409") || !IsTrafficCycleConflict(err) {
		t.Fatalf("cycle conflict = %v", err)
	}

	// An Agent that answers with figures that do not add up is refused too.
	h.trafficErr = nil
	broken, closeBroken := newTestServer(t, mismatchedGrantHandler{fakeHandler: h}, "secret")
	defer closeBroken()
	if _, err := broken.GrantTrafficPackage(context.Background(), grant); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Fatalf("mismatched grant error = %v", err)
	}
}

// mismatchedGrantHandler answers a grant with a package that did not grow by
// the grant, which the server must refuse to relay.
type mismatchedGrantHandler struct{ *fakeHandler }

func (h mismatchedGrantHandler) GrantTrafficPackage(ctx context.Context, grant TrafficPackageGrant) (TrafficUsageUpdate, error) {
	update, err := h.fakeHandler.GrantTrafficPackage(ctx, grant)
	update.Applied.Package.TotalBytes++
	return update, err
}

func TestGrantTrafficPackageOnALegacyAgentIsNotImplemented(t *testing.T) {
	type legacyHandler struct{ Handler }
	client, closeFn := newTestServer(t, legacyHandler{Handler: &fakeHandler{}}, "secret")
	defer closeFn()
	_, err := client.GrantTrafficPackage(context.Background(), TrafficPackageGrant{InBytes: 1, ExpectedCycleStart: 1})
	if err == nil || !strings.Contains(err.Error(), "501") {
		t.Fatalf("legacy Agent error = %v", err)
	}
}

func TestTrafficPackageWireIsStrictAndComplete(t *testing.T) {
	const valid = `{"inBytes":1,"outBytes":2,"totalBytes":3,"expectedCycleStart":1782864000}`
	for name, body := range map[string]string{
		"missing field":  `{"inBytes":1,"outBytes":2,"expectedCycleStart":1782864000}`,
		"unknown field":  `{"inBytes":1,"outBytes":2,"totalBytes":3,"expectedCycleStart":1782864000,"cycle":1}`,
		"empty grant":    `{"inBytes":0,"outBytes":0,"totalBytes":0,"expectedCycleStart":1782864000}`,
		"trailing value": valid + ` {}`,
	} {
		t.Run("grant "+name, func(t *testing.T) {
			h := &fakeHandler{}
			handler := (&Server{Token: "secret", Handler: h}).Mux()
			req := httptest.NewRequest(http.MethodPost, "/api/monitor/package", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || h.packageGrant != (TrafficPackageGrant{}) {
				t.Fatalf("status=%d grant=%+v body=%q", rec.Code, h.packageGrant, rec.Body.String())
			}
		})
	}
	t.Run("grant without a token", func(t *testing.T) {
		h := &fakeHandler{}
		handler := (&Server{Token: "secret", Handler: h}).Mux()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/monitor/package", strings.NewReader(valid)))
		if rec.Code != http.StatusUnauthorized || h.packageGrant != (TrafficPackageGrant{}) {
			t.Fatalf("status=%d grant=%+v", rec.Code, h.packageGrant)
		}
	})

	// The package inside a usage replacement is held to the same standard.
	for name, body := range map[string]string{
		"partial package": `{"inBytes":1,"outBytes":2,"expectedCycleStart":1782864000,"package":{"inBytes":1}}`,
		"unknown package field": `{"inBytes":1,"outBytes":2,"expectedCycleStart":1782864000,` +
			`"package":{"inBytes":1,"outBytes":2,"totalBytes":3,"bonus":4}}`,
	} {
		t.Run("usage "+name, func(t *testing.T) {
			h := &fakeHandler{}
			handler := (&Server{Token: "secret", Handler: h}).Mux()
			req := httptest.NewRequest(http.MethodPut, "/api/monitor/usage", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || h.trafficReq != (TrafficUsageRequest{}) {
				t.Fatalf("status=%d request=%+v body=%q", rec.Code, h.trafficReq, rec.Body.String())
			}
		})
	}
}
