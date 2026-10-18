package aws_lambda

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"pricing-catalog/pkg/catalog"
	"time"
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
	OnDemand map[string]map[string]AwsTerm `json:"onDemand"`
}

type AwsTerm struct {
	PriceDimensions map[string]AwsPriceDimension `json:"priceDimensions"`
}

type AwsPriceDimension struct {
	RateCode     string            `json:"rateCode"`
	Description  string            `json:"description"`
	Unit         string            `json:"unit"`
	PricePerUnit map[string]string `json:"pricePerUnit"`
}

type Connector struct {
	client  *http.Client
	baseUrl string
}

func New(baseUrl string, timeout time.Duration) Connector {
	return Connector{
		client:  &http.Client{Timeout: timeout},
		baseUrl: baseUrl,
	}
}

func (c Connector) Name() string {
	return "aws-lambda"
}

func (c Connector) Fetch(ctx context.Context) ([]catalog.PricingRecord, error) {
	request, err := http.Get(c.baseUrl)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			return
		}
	}(request.Body)

	var data AwsSource
	err = json.NewDecoder(request.Body).Decode(&data)
	if err != nil {
		return nil, err
	}

	records := make([]catalog.PricingRecord, 0, len(data.Products))
	for sku, product := range data.Products {
		terms, ok := data.Terms.OnDemand[sku]
		if !ok {
			continue
		}

		for _, term := range terms {
			for _, dimension := range term.PriceDimensions {
				amount, ok := dimension.PricePerUnit["USD"]
				if !ok {
					continue
				}

				records = append(records, catalog.PricingRecord{
					Provider:      "AWS",
					Product:       product.ProductFamily,
					SKU:           product.SKU,
					Region:        product.Attributes["location"],
					PurchaseModel: "OnDemand",
					BillingUnit:   dimension.Unit,
					Source: catalog.Source{
						Type: "HTTP",
						URL:  c.baseUrl,
					},
					PricingDimension: dimension.Description,
					Price: catalog.Money{
						Amount:   amount,
						Currency: "USD",
					},
				})
			}
		}
	}

	return records, nil
}
