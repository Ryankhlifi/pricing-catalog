package lambda

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pricing-catalog/pkg/catalog"
	"pricing-catalog/pkg/connector"
	"pricing-catalog/pkg/connector/testkit"
)

// testdata/lambda.json holds 19 SKUs; 4 of them have 3 tiers each.
const (
	wantRecords = 27
	testURL     = "https://example.invalid/aws/AWSLambda/index.json"
)

// newFactory gives every call a fresh reader, since a reader can only be
// consumed once.
func newFactory(t *testing.T) func() connector.Connector {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	return func() connector.Connector {
		return New(bytes.NewReader(data), testURL)
	}
}

// find looks a record up by SKU and tier start, because Fetch does not
// guarantee any ordering.
func find(t *testing.T, records []catalog.PricingRecord, sku, start string) catalog.PricingRecord {
	t.Helper()
	for _, r := range records {
		if r.SKU == sku && r.Tier != nil && r.Tier.StartQuantity == start {
			return r
		}
	}
	t.Fatalf("no record for sku %s with tier start %q", sku, start)
	return catalog.PricingRecord{}
}

func TestConnectorContract(t *testing.T) {
	records := testkit.AssertConnectorContract(t, newFactory(t))

	if len(records) != wantRecords {
		t.Fatalf("got %d records, want %d", len(records), wantRecords)
	}

	t.Run("common fields", func(t *testing.T) {
		for _, r := range records {
			if r.Provider != "AWS" || r.PurchaseModel != "OnDemand" {
				t.Errorf("%s: provider/model = %q/%q", r.SKU, r.Provider, r.PurchaseModel)
			}
			if r.Product != "Serverless" {
				t.Errorf("%s: product = %q", r.SKU, r.Product)
			}
			if r.Price.Currency != "USD" || r.Price.Amount == "" {
				t.Errorf("%s: price = %+v", r.SKU, r.Price)
			}
			if r.BillingUnit == "" || r.PricingDimension == "" {
				t.Errorf("%s: empty unit %q or dimension %q", r.SKU, r.BillingUnit, r.PricingDimension)
			}
			if r.Source.Type != "HTTP" || r.Source.URL != testURL {
				t.Errorf("%s: source = %+v", r.SKU, r.Source)
			}
			if r.Tier == nil {
				t.Errorf("%s: tier is nil", r.SKU)
			}
		}
	})

	t.Run("no two records share sku, tier and currency", func(t *testing.T) {
		seen := make(map[string]bool, len(records))
		for _, r := range records {
			key := r.SKU + "|" + r.Tier.StartQuantity + "|" + r.Price.Currency
			if seen[key] {
				t.Errorf("duplicate record for %s", key)
			}
			seen[key] = true
		}
	})

	t.Run("tiers stay distinct and the last one is unbounded", func(t *testing.T) {
		tiers := []struct {
			sku, region, start, end, price, dimension string
		}{
			// us-east-1, x86 duration
			{"TG3M4CAGBA3NYQBH", "us-east-1", "0", "6000000000", "0.0000166667",
				"AWS-Lambda-Duration: AWS Lambda - Total Compute - US East (Northern Virginia)-Tier-1"},
			{"TG3M4CAGBA3NYQBH", "us-east-1", "6000000000", "15000000000", "0.0000150000",
				"AWS-Lambda-Duration: AWS Lambda - Total Compute - US East (Northern Virginia)-Tier-2"},
			{"TG3M4CAGBA3NYQBH", "us-east-1", "15000000000", "", "0.0000133334",
				"AWS-Lambda-Duration: AWS Lambda - Total Compute - US East (Northern Virginia)-Tier-3"},
			// eu-west-3, ARM duration (different thresholds from x86)
			{"RUA4GEMXYRH9896Q", "eu-west-3", "0", "7500000000", "0.0000133334",
				"AWS-Lambda-Duration-ARM: AWS Lambda - Total Compute for ARM - EU (Paris)-Tier-1"},
			{"RUA4GEMXYRH9896Q", "eu-west-3", "7500000000", "18750000000", "0.0000120001",
				"AWS-Lambda-Duration-ARM: AWS Lambda - Total Compute for ARM - EU (Paris)-Tier-2"},
			{"RUA4GEMXYRH9896Q", "eu-west-3", "18750000000", "", "0.0000106667",
				"AWS-Lambda-Duration-ARM: AWS Lambda - Total Compute for ARM - EU (Paris)-Tier-3"},
		}

		for _, want := range tiers {
			r := find(t, records, want.sku, want.start)
			if r.Region != want.region {
				t.Errorf("%s tier %s: region = %q, want %q", want.sku, want.start, r.Region, want.region)
			}
			if r.BillingUnit != "Lambda-GB-Second" {
				t.Errorf("%s tier %s: unit = %q", want.sku, want.start, r.BillingUnit)
			}
			if r.Tier.EndQuantity != want.end {
				t.Errorf("%s tier %s: end = %q, want %q", want.sku, want.start, r.Tier.EndQuantity, want.end)
			}
			if r.Price.Amount != want.price {
				t.Errorf("%s tier %s: price = %q, want %q", want.sku, want.start, r.Price.Amount, want.price)
			}
			if r.PricingDimension != want.dimension {
				t.Errorf("%s tier %s: dimension = %q, want %q", want.sku, want.start, r.PricingDimension, want.dimension)
			}
		}
	})

	t.Run("regions come from regionCode", func(t *testing.T) {
		virginia := find(t, records, "GU2ZS9HVP6QTQ7KE", "0")
		paris := find(t, records, "6GFK8TZW8U8G9MUB", "0")

		if virginia.Region != "us-east-1" || paris.Region != "eu-west-3" {
			t.Errorf("regions = %q / %q", virginia.Region, paris.Region)
		}
		if virginia.BillingUnit != "Requests" || paris.BillingUnit != "Request" {
			t.Errorf("units = %q / %q", virginia.BillingUnit, paris.BillingUnit)
		}
		if virginia.PricingDimension == paris.PricingDimension {
			t.Errorf("same PricingDimension in two regions: %q", virginia.PricingDimension)
		}
	})

	t.Run("ARM and x86 requests are distinguishable", func(t *testing.T) {
		x86 := find(t, records, "GU2ZS9HVP6QTQ7KE", "0")
		arm := find(t, records, "K7BX6567RJ67A2KE", "0")

		if x86.Price.Amount != "0.0000002000" || arm.Price.Amount != "0.0000002000" {
			t.Fatalf("fixture changed: prices %q / %q", x86.Price.Amount, arm.Price.Amount)
		}
		if x86.PricingDimension != "AWS-Lambda-Requests: AWS Lambda - Total Requests - US East (Northern Virginia)" {
			t.Errorf("x86 dimension = %q", x86.PricingDimension)
		}
		if arm.PricingDimension != "AWS-Lambda-Requests-ARM: AWS Lambda - Total Requests for ARM - US East (Northern Virginia)" {
			t.Errorf("ARM dimension = %q", arm.PricingDimension)
		}
	})

	t.Run("global free tier keeps its bounded range and zero price", func(t *testing.T) {
		r := find(t, records, "ZQVCC2M69R4S79XS", "0")

		// These SKUs have an empty regionCode in the source, so Region is Any.
		if r.Region != "Any" {
			t.Errorf("region = %q, want Any", r.Region)
		}
		if r.Price.Amount != "0.0000000000" {
			t.Errorf("price = %q, want zero", r.Price.Amount)
		}
		if r.BillingUnit != "Request" {
			t.Errorf("unit = %q, want the singular \"Request\"", r.BillingUnit)
		}
		if r.Tier.EndQuantity != "1000000" {
			t.Errorf("end = %q, want 1000000", r.Tier.EndQuantity)
		}
		if r.PricingDimension != "AWS-Lambda-Requests: AWS Lambda - Requests Free Tier - 1,000,000 Requests" {
			t.Errorf("dimension = %q", r.PricingDimension)
		}
	})

	t.Run("SKU without a group uses its raw description", func(t *testing.T) {
		r := find(t, records, "228MVDFM77WFS3WF", "0")

		if r.Region != "eu-west-3" || r.BillingUnit != "Hours" || r.Price.Amount != "0.9676800000" {
			t.Errorf("region/unit/price = %q/%q/%q", r.Region, r.BillingUnit, r.Price.Amount)
		}
		want := "$0.96768 per Hour for Lambda-Managed-Instances-m6a.32xlarge-Management-Hours in EU (Paris)"
		if r.PricingDimension != want {
			t.Errorf("dimension = %q, want %q", r.PricingDimension, want)
		}
	})
}

func TestFetchIsDeterministic(t *testing.T) {
	newConn := newFactory(t)

	first, err := newConn().Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := newConn().Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Fetch ranges over maps, so this is expected to fail until the keys are
	// iterated in sorted order.
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two fetches of the same data returned records in a different order")
	}
}

func TestFetchRejectsMalformedJSON(t *testing.T) {
	_, err := New(bytes.NewReader([]byte(`{`)), testURL).Fetch(context.Background())
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}
