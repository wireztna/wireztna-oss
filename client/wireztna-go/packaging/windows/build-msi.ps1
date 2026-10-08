# NON-CANONICAL, FAIL-CLOSED ENTRY POINT
#
# The former WiX 4 flow defined a second product family and omitted contractual
# payloads. It is intentionally disabled rather than producing an incompatible
# MSI. Build prepared payloads with packaging/windows/build-msi.sh, which uses
# wireztna-wixl.wxs as the sole product contract.
$ErrorActionPreference = "Stop"
throw "Disabled non-canonical WiX 4 pipeline. Use packaging/windows/build-msi.sh with wireztna-wixl.wxs."
