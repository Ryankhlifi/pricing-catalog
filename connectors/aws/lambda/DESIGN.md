# AWS Lambda connector design

## Source

The connector reads the public AWS Price List offer file for the `AWSLambda` service. The source is JSON: `products` is keyed by SKU, while `terms.OnDemand` contains offer terms and their `priceDimensions` for each SKU. See the [AWS Lambda pricing page](https://aws.amazon.com/lambda/pricing/) and the [AWS Price List documentation](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/price-changes.html).

The connector is configured with a source URL and handles fetching and decoding the response. The provider-specific structs model only the fields needed for normalization rather than mirroring every field in AWS's deeply nested response.

## Pricing model

The source represents several separately metered Lambda charges, including:

- invocation requests;
- invocation compute duration, measured in GB-seconds, including ARM-specific rates;
- provisioned concurrency duration;
- ephemeral-storage duration;
- processed bytes for streamed responses;
- Lambda Managed Instances requests and management hours, where present in the source.

The implementation currently converts `OnDemand` terms. Each source price dimension becomes its own canonical pricing record, with a separate record for each currency in `pricePerUnit`.

## Mapping decisions

- **Provider and product:** `Provider` is `AWS`; `Product` comes from the source `productFamily` (for example, `Serverless`).
- **SKU:** The product's AWS SKU is retained as the canonical `SKU`.
- **Region:** `regionCode` becomes `Region`. An empty region code is represented as `Any`, which is used for global entries in the fixture.
- **Purchase model:** Records are labelled `OnDemand`, because the connector reads `terms.OnDemand` only.
- **Pricing dimension:** When the product has a `group`, the canonical dimension is `group + ": " + description`. Otherwise, the source description is used directly. Keeping both preserves distinctions such as request versus duration pricing and ARM-specific meters without inventing a provider-independent meaning for them.
- **Billing unit:** The source `unit` is retained as-is. Values such as `Request` and `Requests` are not silently rewritten.
- **Tiers:** `beginRange` maps to `Tier.StartQuantity` and `endRange` maps to `Tier.EndQuantity`. AWS's `Inf` end range is represented by an empty end quantity to denote an unbounded upper tier. Bounded ranges and zero-priced free-tier dimensions are retained.
- **Price and currency:** The amount is kept as the source decimal string rather than converted through a floating-point number. The source currency is copied to `Price.Currency`.
- **Source metadata:** The configured source URL is recorded as an HTTP source on each canonical record.
- **Determinism:** SKU, term, and price-dimension map keys are traversed in sorted order so a fixed source document produces stable record ordering.

AWS-specific fields are not all promoted into separate canonical fields. In particular, architecture and meter distinctions are preserved through the SKU and pricing-dimension text where the source exposes them.

## Testing

Tests use a deterministic JSON fixture rather than relying on live AWS pricing. HTTP behavior can be exercised with a local mock HTTP server, keeping tests independent of network access to AWS. The tests cover the connector contract, expected record count and common fields, tier boundaries, region mapping, ARM versus non-ARM dimensions, global free-tier pricing, fallback to the raw description when no group is present, malformed JSON, and repeatable output.

## Limitations

- Only `OnDemand` terms are normalized. Other purchase-term types are outside the current implementation.
- The connector retains the source's tier ranges and units; it does not aggregate usage, convert units, calculate a complete Lambda bill, or combine meters into a single feature-level price.
- AWS's `appliesTo` relationships are not modeled. A free-tier dimension is retained as its own source pricing record rather than being linked to every SKU it may apply to.
- Only a subset of product attributes is used directly. Fields such as `location`, `usagetype`, `groupDescription`, and other product attributes are not independently represented in the canonical record.
- The implementation preserves source prices and currencies; it does not calculate currency conversions, discounts, taxes, or costs from other AWS services.

## AI assistance

AI tools were used to help write and refine tests and to simplify the representation of AWS's heavily nested pricing JSON into focused provider-specific structs. The generated suggestions were reviewed and adapted to the connector's actual mapping decisions; the fixture-based tests are used to check the resulting behavior.
