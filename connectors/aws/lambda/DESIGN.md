# AWS Lambda connector design

## Source

The connector consumes the AWS Price List Bulk API JSON offer file for the `AWSLambda` service. AWS publishes service offer files in JSON and CSV formats; this connector expects the JSON structure with `products` and `terms` sections. See the [AWS Price List Bulk API documentation](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/using-the-aws-price-list-bulk-api-fetching-price-list-files-manually.html) and the [AWS Lambda pricing page](https://aws.amazon.com/lambda/pricing/).

The source URL is supplied to `New`. `Fetch` performs an HTTP GET with an `Accept: application/json` header, checks the response status, then passes the response body to the normalizer. Keeping normalization in `normalizeAWS(io.Reader, source)` lets the JSON transformation be tested independently of the HTTP request while the connector tests can still exercise the full fetch path.

The provider-specific structs represent only the source fields needed by the mapping. They do not attempt to reproduce the full AWS offer-file schema.

## Pricing model

AWS Lambda prices are represented as individual meters rather than as one combined price for a Lambda function. The fixture includes examples of:

- invocation requests, including ARM-specific rates;
- invocation compute duration measured in GB-seconds, including ARM-specific rates and tiered prices;
- provisioned concurrency duration;
- ephemeral-storage duration;
- processed bytes for streamed responses;
- Lambda Managed Instances requests and management hours;
- global free-tier dimensions.

The connector reads `terms.OnDemand`. For each product SKU, it finds the matching On-Demand terms, then emits one canonical record per price dimension and per currency in that dimension's `pricePerUnit` map. Products without matching On-Demand terms are skipped.

## Mapping decisions

- **Provider and product:** `Provider` is set to `AWS`; `Product` comes from `productFamily` (the fixture uses `Serverless`).
- **SKU:** The AWS product SKU is retained in the canonical `SKU` field.
- **Region:** `attributes.regionCode` maps to `Region`. If the source region code is empty, the connector uses `Any`, which is how global entries in the fixture are represented.
- **Purchase model:** The canonical `PurchaseModel` is `OnDemand` because the connector only reads `terms.OnDemand`.
- **Pricing dimension:** If `attributes.group` is present, the connector prefixes the source description with `group + ": "`. If the group is absent, the description is preserved as-is. This retains useful distinctions, such as ARM-specific meters, without adding AWS-only fields to the canonical schema.
- **Billing unit:** The source `unit` is retained unchanged. Values such as `Request` and `Requests` are not normalized into one spelling.
- **Tiers:** `beginRange` maps to `Tier.StartQuantity` and `endRange` maps to `Tier.EndQuantity`. AWS's `Inf` end range becomes an empty end quantity to represent an unbounded upper tier. Quantities remain strings rather than being parsed into numeric types.
- **Price and currency:** Each entry in `pricePerUnit` becomes a separate record. The source amount is retained as a string, avoiding floating-point conversion, and the map key is copied into `Price.Currency`.
- **Source metadata:** The configured source URL is stored in the canonical record as an HTTP source.
- **Deterministic output:** Product SKUs, term keys, price-dimension keys, and currency keys are sorted before iteration, so the same input produces records in a stable order.

## Testing

The tests use a checked-in JSON fixture, so they do not depend on AWS availability or live pricing changes. An `httptest` server serves the fixture to exercise the connector's HTTP request path. Additional normalization tests can pass an in-memory reader directly to `normalizeAWS`.

Coverage includes the connector contract and expected record count, common canonical fields, tier boundaries and prices, regional versus global entries, ARM versus non-ARM dimensions, preservation of source billing-unit spelling, zero-priced free-tier records, descriptions without a group, multi-currency ordering, deterministic output, malformed JSON, non-success HTTP responses, the GET method and JSON `Accept` header, and request-context cancellation.

## Limitations

- Only On-Demand terms are normalized. Other pricing-term types are not included.
- Source fields such as `effectiveDate`, `rateCode`, `appliesTo`, and `termAttributes` are not represented in the canonical records. In particular, free-tier dimensions are emitted as their own records; their `appliesTo` relationships are not linked to the SKUs they cover.
- Only selected product attributes are used directly. Attributes such as `location`, `usagetype`, `groupDescription`, architecture-related metadata, and other provider-specific fields are not independently represented. Distinctions exposed in descriptions and groups remain in `PricingDimension`, and the provider SKU is preserved.
- The connector does not aggregate usage, convert billing units, combine meters into an estimated bill, or calculate a complete Lambda workload cost.
- It does not model discounts, taxes, currency conversions, or charges from other AWS services. AWS's published offer-file prices are informational; the applicable service pricing terms should be checked when estimating actual charges.

## AI assistance

AI tools were used to help write and refine tests and simplify the representation of AWS's heavily nested pricing JSON into focused Go structs. The connector's mapping remains provider-specific, and fixture-backed tests check the resulting behavior and important normalization cases.
