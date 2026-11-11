# Java consistent56 reference

The checked-in JSON fixture was produced by the Java sampler used by the
pinned EDOT Java source. It covers 210 combinations of root/remote parent,
probability, threshold, explicit randomness and malformed vendor state.

The expected values are computed by OpenTelemetry Java Contrib
`opentelemetry-consistent-sampling:1.61.0-alpha` with OTel Java 1.66.0.
`reference.json` records exact artifact SHA-512 hashes. Go tests consume the
JSON fixture and run without Java or network access.

To regenerate with a JDK installed, run from the repository root

```sh
python3 scripts/generate_consistent56_vectors.py
```

The generator uses temporary files, verifies artifact hashes, and replaces
`testdata/consistent56-java.json`. The source is Apache-2.0 licensed.
