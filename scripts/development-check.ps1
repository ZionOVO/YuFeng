param(
    [ValidateRange(1, 256)]
    [int]$Parallelism = 1
)

$ErrorActionPreference = 'Stop'
$packagePatterns = @(
    './agents/...',
    './cmd/...',
    './components/...',
    './console',
    './deploy',
    './docs',
    './lib/...',
    './procedures/...',
    './proto/...',
    './scripts/...'
)

go build @packagePatterns
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go test -p $Parallelism @packagePatterns
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go test -p $Parallelism -tags yufeng_dev ./cmd/yufeng-brain ./cmd/yufeng-edge ./cmd/yfctl
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go vet @packagePatterns
exit $LASTEXITCODE
