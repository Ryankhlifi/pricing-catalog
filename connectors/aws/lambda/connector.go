package lambda

import (
	"context"
	"encoding/json"
	"io"
	"pricing-catalog/pkg/catalog"
	"sort"
)

type AwsSource struct {
	Products map[string]AwsProduct
	Terms    AwsTerms
}

type AwsProduct struct {
	SKU           string            `json:"sku"`
	ProductFamily string            `json:"productFamily"`
	Attributes    map[string]string `json:"attributes"`
}

type AwsTerms struct {
	OnDemand map[string]map[string]AwsTerm `json:"OnDemand"`
}

type AwsTerm struct {
	PriceDimensions map[string]AwsPriceDimension `json:"priceDimensions"`
}

type AwsPriceDimension struct {
	RateCode     string            `json:"rateCode"`
	Description  string            `json:"description"`
	BeginRange   string            `json:"beginRange"`
	EndRange     string            `json:"endRange"`
	Unit         string            `json:"unit"`
	PricePerUnit map[string]string `json:"pricePerUnit"`
}

type Connector struct {
	Reader  io.Reader
	BaseURL string
}

func New(reader io.Reader, url string) Connector {
	return Connector{
		Reader:  reader,
		BaseURL: url,
	}
}

func (c Connector) Name() string {
	return "aws-lambda"
}

func (c Connector) Fetch(ctx context.Context) ([]catalog.PricingRecord, error) {

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var data AwsSource
	err := json.NewDecoder(c.Reader).Decode(&data)
	if err != nil {
		return nil, err
	}

	records := make([]catalog.PricingRecord, 0, len(data.Products))

	for _, sku := range sortedKeys(data.Products) {
		product := data.Products[sku]
		terms, ok := data.Terms.OnDemand[sku]
		if !ok {
			continue
		}

		for _, termKey := range sortedKeys(terms) {
			term := terms[termKey]

			for _, dimKey := range sortedKeys(term.PriceDimensions) {
				dimension := term.PriceDimensions[dimKey]

				var pricingDimension string
				if group := product.Attributes["group"]; group != "" {
					pricingDimension = group + ": " + dimension.Description
				} else {
					pricingDimension = dimension.Description
				}

				endQuantity := dimension.EndRange
				if endQuantity == "Inf" {
					endQuantity = ""
				}

				region := product.Attributes["regionCode"]
				if region == "" {
					region = "Any"
				}

				for currency, amount := range dimension.PricePerUnit {
					records = append(records, catalog.PricingRecord{
						Provider:      "AWS",
						Product:       product.ProductFamily,
						SKU:           product.SKU,
						Region:        region,
						PurchaseModel: "OnDemand",
						BillingUnit:   dimension.Unit,
						Source: catalog.Source{
							Type: "HTTP",
							URL:  c.BaseURL,
						},
						PricingDimension: pricingDimension,
						Tier: &catalog.Tier{
							StartQuantity: dimension.BeginRange,
							EndQuantity:   endQuantity,
						},
						Price: catalog.Money{
							Amount:   amount,
							Currency: currency,
						},
					})
				}
			}
		}
	}

	return records, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
