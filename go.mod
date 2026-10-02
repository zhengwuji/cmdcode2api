module cmdcode2api

go 1.25

// Pins the minimum patch release so every build (local, CI, Docker) uses a
// toolchain that has the fixes the go directive alone does not guarantee.
toolchain go1.25.0

require gopkg.in/yaml.v3 v3.0.1
