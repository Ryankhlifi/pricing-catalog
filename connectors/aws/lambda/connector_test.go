package lambda

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"pricing-catalog/pkg/catalog"
	"pricing-catalog/pkg/connector"
	"pricing-catalog/pkg/connector/testkit"
)

const wantRecords = 27

// fixtureData loads the captured AWS Lambda price-list fixture. Tests never
// depend on AWS or any other external service being available.
func fixtureData(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "data.json"))
	if err != nil {
		t.Fatalf("read AWS Lambda fixture: %v", err)
	}
	return data
}

// newFixtureServer serves the fixture from a local HTTP server so the tests
// exercise the connector's HTTP acquisition path without making network calls.
func newFixtureServer(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

// newLambdaConnector centralizes construction so the tests only need to change
// this helper if the connector constructor changes.
func newLambdaConnector(sourceURL string) Connector {
	return New(sourceURL)
}

// newFactory gives each contract-test invocation a connector pointed at the
// same local fixture server.
func newFactory(t *testing.T) (func() connector.Connector, string) {
	t.Helper()
	server := newFixtureServer(t, http.StatusOK, fixtureData(t))

	return func() connector.Connector {
		return newLambdaConnector(server.URL)
	}, server.URL
}

// find locates a record by SKU and tier start. Tests should not depend on map
// iteration order; output determinism is checked independently below.
func find(t *testing.T, records []catalog.PricingRecord, sku, start string) catalog.PricingRecord {
	t.Helper()

	for _, record := range records {
		if record.SKU == sku && record.Tier != nil && record.Tier.StartQuantity == start {
			return record
		}
	}

	t.Fatalf("no record for SKU %q with tier start %q", sku, start)
	return catalog.PricingRecord{}
}

func TestConnectorContract(t *testing.T) {
	factory, sourceURL := newFactory(t)
	records := testkit.AssertConnectorContract(t, factory)

	if len(records) != wantRecords {
		t.Fatalf("got %d records, want %d", len(records), wantRecords)
	}

	t.Run("common fields are populated", func(t *testing.T) {
		for _, record := range records {
			if record.Provider != "AWS" || record.PurchaseModel != "OnDemand" {
				t.Errorf("SKU %s: provider/model = %q/%q", record.SKU, record.Provider, record.PurchaseModel)
			}
			if record.Product != "Serverless" {
				t.Errorf("SKU %s: product = %q, want %q", record.SKU, record.Product, "Serverless")
			}
			if record.SKU == "" {
				t.Errorf("record has an empty SKU: %+v", record)
			}
			if record.Price.Currency != "USD" || record.Price.Amount == "" {
				t.Errorf("SKU %s: price = %+v", record.SKU, record.Price)
			}
			if record.BillingUnit == "" || record.PricingDimension == "" {
				t.Errorf("SKU %s: empty unit %q or pricing dimension %q", record.SKU, record.BillingUnit, record.PricingDimension)
			}
			if record.Source.Type != "HTTP" || record.Source.URL != sourceURL {
				t.Errorf("SKU %s: source = %+v, want HTTP source URL %q", record.SKU, record.Source, sourceURL)
			}
			if record.Tier == nil {
				t.Errorf("SKU %s: tier is nil", record.SKU)
			}
		}
	})

	t.Run("records are unique by SKU, tier start and currency", func(t *testing.T) {
		seen := make(map[string]struct{}, len(records))
		for _, record := range records {
			if record.Tier == nil {
				continue
			}
			key := record.SKU + "|" + record.Tier.StartQuantity + "|" + record.Price.Currency
			if _, exists := seen[key]; exists {
				t.Errorf("duplicate record for %s", key)
			}
			seen[key] = struct{}{}
		}
	})

	t.Run("tiered duration prices preserve ranges and amounts", func(t *testing.T) {
		cases := []struct {
			name, sku, region, start, end, price, dimension string
		}{
			{
				name: "x86 tier 1", sku: "TG3M4CAGBA3NYQBH", region: "us-east-1",
				start: "0", end: "6000000000", price: "0.0000166667",
				dimension: "AWS-Lambda-Duration: AWS Lambda - Total Compute - US East (Northern Virginia)-Tier-1",
			},
			{
				name: "x86 tier 2", sku: "TG3M4CAGBA3NYQBH", region: "us-east-1",
				start: "6000000000", end: "15000000000", price: "0.0000150000",
				dimension: "AWS-Lambda-Duration: AWS Lambda - Total Compute - US East (Northern Virginia)-Tier-2",
			},
			{
				name: "x86 tier 3", sku: "TG3M4CAGBA3NYQBH", region: "us-east-1",
				start: "15000000000", end: "", price: "0.0000133334",
				dimension: "AWS-Lambda-Duration: AWS Lambda - Total Compute - US East (Northern Virginia)-Tier-3",
			},
			{
				name: "ARM tier 1", sku: "RUA4GEMXYRH9896Q", region: "eu-west-3",
				start: "0", end: "7500000000", price: "0.0000133334",
				dimension: "AWS-Lambda-Duration-ARM: AWS Lambda - Total Compute for ARM - EU (Paris)-Tier-1",
			},
			{
				name: "ARM tier 2", sku: "RUA4GEMXYRH9896Q", region: "eu-west-3",
				start: "7500000000", end: "18750000000", price: "0.0000120001",
				dimension: "AWS-Lambda-Duration-ARM: AWS Lambda - Total Compute for ARM - EU (Paris)-Tier-2",
			},
			{
				name: "ARM tier 3", sku: "RUA4GEMXYRH9896Q", region: "eu-west-3",
				start: "18750000000", end: "", price: "0.0000106667",
				dimension: "AWS-Lambda-Duration-ARM: AWS Lambda - Total Compute for ARM - EU (Paris)-Tier-3",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				record := find(t, records, tc.sku, tc.start)
				if record.Region != tc.region {
					t.Errorf("region = %q, want %q", record.Region, tc.region)
				}
				if record.BillingUnit != "Lambda-GB-Second" {
					t.Errorf("unit = %q, want %q", record.BillingUnit, "Lambda-GB-Second")
				}
				if record.Tier.EndQuantity != tc.end {
					t.Errorf("end quantity = %q, want %q", record.Tier.EndQuantity, tc.end)
				}
				if record.Price.Amount != tc.price {
					t.Errorf("price = %q, want %q", record.Price.Amount, tc.price)
				}
				if record.PricingDimension != tc.dimension {
					t.Errorf("pricing dimension = %q, want %q", record.PricingDimension, tc.dimension)
				}
			})
		}
	})

	t.Run("regional request prices preserve region and unit", func(t *testing.T) {
		virginia := find(t, records, "GU2ZS9HVP6QTQ7KE", "0")
		paris := find(t, records, "6GFK8TZW8U8G9MUB", "0")

		if virginia.Region != "us-east-1" || paris.Region != "eu-west-3" {
			t.Errorf("regions = %q / %q, want us-east-1 / eu-west-3", virginia.Region, paris.Region)
		}
		if virginia.BillingUnit != "Requests" || paris.BillingUnit != "Request" {
			t.Errorf("units = %q / %q, want Requests / Request", virginia.BillingUnit, paris.BillingUnit)
		}
		if virginia.PricingDimension == paris.PricingDimension {
			t.Errorf("pricing dimensions unexpectedly match: %q", virginia.PricingDimension)
		}
	})

	t.Run("ARM and x86 request dimensions stay distinct", func(t *testing.T) {
		x86 := find(t, records, "GU2ZS9HVP6QTQ7KE", "0")
		arm := find(t, records, "K7BX6567RJ67A2KE", "0")

		if x86.Price.Amount != "0.0000002000" || arm.Price.Amount != "0.0000002000" {
			t.Fatalf("fixture changed: prices = %q / %q", x86.Price.Amount, arm.Price.Amount)
		}
		if x86.PricingDimension != "AWS-Lambda-Requests: AWS Lambda - Total Requests - US East (Northern Virginia)" {
			t.Errorf("x86 dimension = %q", x86.PricingDimension)
		}
		if arm.PricingDimension != "AWS-Lambda-Requests-ARM: AWS Lambda - Total Requests for ARM - US East (Northern Virginia)" {
			t.Errorf("ARM dimension = %q", arm.PricingDimension)
		}
	})

	t.Run("global free tier preserves zero price and bounded range", func(t *testing.T) {
		record := find(t, records, "ZQVCC2M69R4S79XS", "0")

		if record.Region != "Any" {
			t.Errorf("region = %q, want Any", record.Region)
		}
		if record.Price.Amount != "0.0000000000" {
			t.Errorf("price = %q, want 0.0000000000", record.Price.Amount)
		}
		if record.BillingUnit != "Request" {
			t.Errorf("unit = %q, want Request", record.BillingUnit)
		}
		if record.Tier.EndQuantity != "1000000" {
			t.Errorf("end quantity = %q, want 1000000", record.Tier.EndQuantity)
		}
		if record.PricingDimension != "AWS-Lambda-Requests: AWS Lambda - Requests Free Tier - 1,000,000 Requests" {
			t.Errorf("dimension = %q", record.PricingDimension)
		}
	})

	t.Run("product without group keeps raw description", func(t *testing.T) {
		record := find(t, records, "228MVDFM77WFS3WF", "0")

		if record.Region != "eu-west-3" || record.BillingUnit != "Hours" || record.Price.Amount != "0.9676800000" {
			t.Errorf("region/unit/price = %q/%q/%q", record.Region, record.BillingUnit, record.Price.Amount)
		}
		wantDimension := "$0.96768 per Hour for Lambda-Managed-Instances-m6a.32xlarge-Management-Hours in EU (Paris)"
		if record.PricingDimension != wantDimension {
			t.Errorf("dimension = %q, want %q", record.PricingDimension, wantDimension)
		}
	})
}

func TestFetchIsDeterministic(t *testing.T) {
	factory, _ := newFactory(t)
	first, err := factory().Fetch(context.Background())
	if err != nil {
		t.Fatalf("first Fetch() returned error: %v", err)
	}
	second, err := factory().Fetch(context.Background())
	if err != nil {
		t.Fatalf("second Fetch() returned error: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatal("Fetch returned records in a different order for identical source data")
	}
}

func TestFetchUsesGETAndRequestsJSON(t *testing.T) {
	body := []byte(`{"products":{},"terms":{"OnDemand":{}}}`)
	requestChecked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("HTTP method = %q, want GET", r.Method)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept header = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		close(requestChecked)
	}))
	t.Cleanup(server.Close)

	if _, err := newLambdaConnector(server.URL).Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	select {
	case <-requestChecked:
	case <-time.After(2 * time.Second):
		t.Fatal("request handler was not called")
	}
}

func TestFetchRejectsNon2xxStatus(t *testing.T) {
	// A valid JSON body ensures this test fails specifically when the connector
	// ignores the HTTP status code, rather than because decoding fails.
	body := []byte(`{"products":{},"terms":{"OnDemand":{}}}`)
	server := newFixtureServer(t, http.StatusBadGateway, body)

	_, err := newLambdaConnector(server.URL).Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch succeeded for an HTTP 502 response; want an error")
	}
}

func TestFetchRejectsMalformedJSON(t *testing.T) {
	server := newFixtureServer(t, http.StatusOK, []byte(`{"products":`))

	_, err := newLambdaConnector(server.URL).Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch succeeded for malformed JSON; want an error")
	}
}

func TestFetchHonorsContextCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetchDone := make(chan error, 1)
	go func() {
		_, err := newLambdaConnector(server.URL).Fetch(ctx)
		fetchDone <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request did not reach the test server")
	}
	cancel()

	select {
	case err := <-fetchDone:
		if err == nil {
			t.Fatal("Fetch succeeded after its context was cancelled; want an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Fetch did not return after its context was cancelled")
	}
}

func TestNormalizeAWSRejectsMalformedJSON(t *testing.T) {
	_, err := normalizeAWS(bytes.NewReader([]byte(`{"products":`)), "")
	if err == nil {
		t.Fatal("normalizeAWS succeeded for malformed JSON; want an error")
	}
}

func TestNormalizeAWSOrdersCurrencies(t *testing.T) {
	// A synthetic source containing several currencies makes currency ordering
	// testable without relying on the production fixture to contain them.
	source := AwsSource{
		Products: map[string]AwsProduct{
			"SKU-1": {
				SKU:           "SKU-1",
				ProductFamily: "Serverless",
				Attributes:    map[string]string{"regionCode": "us-east-1", "group": "Requests"},
			},
		},
		Terms: AwsTerms{
			OnDemand: map[string]map[string]AwsTerm{
				"SKU-1": {
					"TERM-1": {
						PriceDimensions: map[string]AwsPriceDimension{
							"RATE-1": {
								RateCode:     "RATE-1",
								Description:  "Lambda requests",
								BeginRange:   "0",
								EndRange:     "Inf",
								Unit:         "Request",
								PricePerUnit: map[string]string{"USD": "0.0000002", "EUR": "0.0000003", "JPY": "0.00003"},
							},
						},
					},
				},
			},
		},
	}

	input, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal synthetic source: %v", err)
	}

	records, err := normalizeAWS(bytes.NewReader(input), "")
	if err != nil {
		t.Fatalf("normalizeAWS returned error: %v", err)
	}

	gotCurrencies := make([]string, 0, len(records))
	for _, record := range records {
		gotCurrencies = append(gotCurrencies, record.Price.Currency)
	}
	wantCurrencies := []string{"EUR", "JPY", "USD"}
	if !reflect.DeepEqual(gotCurrencies, wantCurrencies) {
		t.Fatalf("currency order = %v, want %v", gotCurrencies, wantCurrencies)
	}
}
