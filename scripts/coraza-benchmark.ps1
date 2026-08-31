[CmdletBinding()]
param(
    [ValidatePattern('^[1-9][0-9]*(ns|us|ms|s|x)$')]
    [string]$Benchtime = '3s',

    [ValidateRange(1, 20)]
    [int]$Count = 5,

    [ValidateNotNullOrEmpty()]
    [int[]]$ProcessorCounts = @(1, 32)
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$officialModule = 'github.com/corazawaf/coraza/v3@v3.7.0'
$maintainedModule = 'github.com/ZionOVO/coraza/v3@v3.7.1-0.20260831022307-151f051001b8'
$benchmarkPattern = '^BenchmarkCoraza(DetectorSerial|DetectorParallel|ReleaseProxyCapacityParallel)$'
$goldenPattern = '^TestCorazaMaintainedEngine(PreservesOfficialDetectionGolden|SupportsOwnedLifecycle)$'
$utf8 = [System.Text.UTF8Encoding]::new($false)

function Write-Utf8Text {
    param(
        [Parameter(Mandatory)] [string]$Path,
        [Parameter(Mandatory)] [string]$Text
    )

    [System.IO.File]::WriteAllText($Path, $Text, $utf8)
}

function Write-Utf8Lines {
    param(
        [Parameter(Mandatory)] [string]$Path,
        [Parameter(Mandatory)] [string[]]$Lines
    )

    [System.IO.File]::WriteAllLines($Path, $Lines, $utf8)
}

function Invoke-CapturedNative {
    param(
        [Parameter(Mandatory)] [string]$FilePath,
        [Parameter(Mandatory)] [string[]]$Arguments,
        [Parameter(Mandatory)] [string]$WorkingDirectory,
        [string]$OutputPath
    )

    Push-Location $WorkingDirectory
    try {
        $lines = @(& $FilePath @Arguments 2>&1 | ForEach-Object { $_.ToString() })
        $exitCode = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
    if ($OutputPath) {
        Write-Utf8Lines -Path $OutputPath -Lines $lines
    }
    foreach ($line in $lines) {
        Write-Host $line
    }
    return [pscustomobject]@{
        ExitCode = $exitCode
        Lines    = $lines
    }
}

function Invoke-RequiredNative {
    param(
        [Parameter(Mandatory)] [string]$FilePath,
        [Parameter(Mandatory)] [string[]]$Arguments,
        [Parameter(Mandatory)] [string]$WorkingDirectory,
        [string]$OutputPath
    )

    $result = Invoke-CapturedNative -FilePath $FilePath -Arguments $Arguments -WorkingDirectory $WorkingDirectory -OutputPath $OutputPath
    if ($result.ExitCode -ne 0) {
        throw "$FilePath failed with exit code $($result.ExitCode)"
    }
    return $result
}

function Get-Median {
    param([Parameter(Mandatory)] [double[]]$Values)

    $ordered = @($Values | Sort-Object)
    if ($ordered.Count -eq 0) {
        throw 'median requires at least one value'
    }
    $middle = [math]::Floor($ordered.Count / 2)
    if (($ordered.Count % 2) -eq 1) {
        return [double]$ordered[$middle]
    }
    return ([double]$ordered[$middle - 1] + [double]$ordered[$middle]) / 2
}

function Convert-BenchmarkLines {
    param(
        [Parameter(Mandatory)] [string]$Variant,
        [Parameter(Mandatory)] [string[]]$Lines,
        [Parameter(Mandatory)] [int[]]$ConfiguredProcessors
    )

    $numberFormat = [System.Globalization.CultureInfo]::InvariantCulture
    $rows = @()
    foreach ($line in $Lines) {
        if ($line -notmatch '^(Benchmark\S+)\s+\d+\s+([0-9.]+)\s+ns/op(?:\s+([0-9.]+)\s+MB/s)?\s+([0-9]+)\s+B/op\s+([0-9]+)\s+allocs/op$') {
            continue
        }
        $benchmarkWithProcessors = $Matches[1]
        if ($benchmarkWithProcessors -match '^(.*)-([0-9]+)$') {
            $benchmark = $Matches[1]
            $processors = [int]$Matches[2]
        }
        elseif ($ConfiguredProcessors.Count -eq 1) {
            $benchmark = $benchmarkWithProcessors
            $processors = $ConfiguredProcessors[0]
        }
        else {
            throw "benchmark line has no processor suffix for a multi-processor run: $line"
        }
        $nsPerOperation = [double]::Parse(($line -replace '^Benchmark\S+\s+\d+\s+([0-9.]+).+$', '$1'), $numberFormat)
        $metricMatch = [regex]::Match($line, '^Benchmark\S+\s+\d+\s+([0-9.]+)\s+ns/op(?:\s+([0-9.]+)\s+MB/s)?\s+([0-9]+)\s+B/op\s+([0-9]+)\s+allocs/op$')
        $mbPerSecond = $null
        if ($metricMatch.Groups[2].Success) {
            $mbPerSecond = [double]::Parse($metricMatch.Groups[2].Value, $numberFormat)
        }
        $rows += [pscustomobject]@{
            variant              = $Variant
            benchmark            = $benchmark
            processors           = $processors
            ns_per_operation     = $nsPerOperation
            megabytes_per_second = $mbPerSecond
            bytes_per_operation  = [double]::Parse($metricMatch.Groups[3].Value, $numberFormat)
            allocations          = [double]::Parse($metricMatch.Groups[4].Value, $numberFormat)
        }
    }
    if ($rows.Count -eq 0) {
        throw "no benchmark rows parsed for $Variant"
    }
    return $rows
}

function Get-BenchmarkMedians {
    param(
        [Parameter(Mandatory)] [object[]]$Rows,
        [Parameter(Mandatory)] [int]$ExpectedCount
    )

    $medians = @()
    foreach ($group in ($Rows | Group-Object { "$($_.variant)|$($_.benchmark)|$($_.processors)" })) {
        if ($group.Count -ne $ExpectedCount) {
            throw "$($group.Name) has $($group.Count) runs, expected $ExpectedCount"
        }
        $first = $group.Group[0]
        $medianNS = Get-Median -Values @($group.Group | ForEach-Object { [double]$_.ns_per_operation })
        $throughputValues = @($group.Group | Where-Object { $null -ne $_.megabytes_per_second } | ForEach-Object { [double]$_.megabytes_per_second })
        $medianThroughput = $null
        if ($throughputValues.Count -gt 0) {
            $medianThroughput = Get-Median -Values $throughputValues
        }
        $medians += [pscustomobject]@{
            variant                      = $first.variant
            benchmark                    = $first.benchmark
            processors                   = $first.processors
            runs                         = $group.Count
            median_ns_per_operation       = $medianNS
            requests_per_second           = 1000000000.0 / $medianNS
            median_megabytes_per_second   = $medianThroughput
            median_bytes_per_operation    = Get-Median -Values @($group.Group | ForEach-Object { [double]$_.bytes_per_operation })
            median_allocations_per_request = Get-Median -Values @($group.Group | ForEach-Object { [double]$_.allocations })
        }
    }
    return @($medians | Sort-Object benchmark, processors, variant)
}

function Get-PercentChange {
    param(
        [Parameter(Mandatory)] [double]$Before,
        [Parameter(Mandatory)] [double]$After
    )

    if ($Before -eq 0) {
        return $null
    }
    return (($After - $Before) / $Before) * 100
}

if ($env:OS -ne 'Windows_NT') {
    throw 'the Coraza benchmark is fixed to Windows'
}
foreach ($processors in $ProcessorCounts) {
    if ($processors -lt 1 -or $processors -gt 256) {
        throw "invalid processor count $processors"
    }
}
$ProcessorCounts = @($ProcessorCounts | Sort-Object -Unique)

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repositoryRoot = (Resolve-Path (Join-Path $scriptRoot '..')).Path
$timestamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$outputRoot = Join-Path $repositoryRoot ".tmp/coraza-benchmark-$timestamp"
if (Test-Path -LiteralPath $outputRoot) {
    throw "benchmark output already exists: $outputRoot"
}
New-Item -ItemType Directory -Path $outputRoot | Out-Null

$archivePath = Join-Path $outputRoot 'yufeng-source.zip'
# git archive 只复制当前提交中的跟踪文件，不读取其它工作树或本地 Coraza 目录。
Invoke-RequiredNative -FilePath 'git' -Arguments @('archive', '--format=zip', "--output=$archivePath", 'HEAD') -WorkingDirectory $repositoryRoot | Out-Null

$officialSource = Join-Path $outputRoot 'official-source'
$maintainedSource = Join-Path $outputRoot 'maintained-source'
Expand-Archive -LiteralPath $archivePath -DestinationPath $officialSource
Expand-Archive -LiteralPath $archivePath -DestinationPath $maintainedSource

# 官方临时副本等价执行 go mod edit -dropreplace；正式源码仍保留远端不可变替换。
Invoke-RequiredNative -FilePath 'go' -Arguments @('mod', 'edit', '-dropreplace=github.com/corazawaf/coraza/v3') -WorkingDirectory $officialSource | Out-Null
$officialCorazaPath = Join-Path $officialSource 'lib/edgecore/coraza.go'
$officialCorazaSource = [System.IO.File]::ReadAllText($officialCorazaPath)
$officialCorazaSourceWithoutDirective = [regex]::Replace($officialCorazaSource, '(?m)^SecRxPreFilter Off\r?\n', '', 1)
if ($officialCorazaSourceWithoutDirective -eq $officialCorazaSource) {
    throw 'official source copy did not contain SecRxPreFilter Off'
}
Write-Utf8Text -Path $officialCorazaPath -Text $officialCorazaSourceWithoutDirective
Invoke-RequiredNative -FilePath 'go' -Arguments @('mod', 'tidy') -WorkingDirectory $officialSource | Out-Null

# 两份源码都执行 go mod verify，确保下载内容与校验和一致。
Invoke-RequiredNative -FilePath 'go' -Arguments @('mod', 'verify') -WorkingDirectory $officialSource | Out-Null
Invoke-RequiredNative -FilePath 'go' -Arguments @('mod', 'verify') -WorkingDirectory $maintainedSource | Out-Null

$operatingSystem = Get-CimInstance Win32_OperatingSystem
$processor = Get-CimInstance Win32_Processor | Select-Object -First 1
$powerPlan = @(& powercfg /getactivescheme 2>&1 | ForEach-Object { $_.ToString() }) -join "`n"
$goVersion = @(& go version 2>&1 | ForEach-Object { $_.ToString() }) -join "`n"
$goEnvironment = @(& go env GOOS GOARCH CGO_ENABLED 2>&1 | ForEach-Object { $_.ToString() })
$sourceCommit = @(& git -C $repositoryRoot rev-parse HEAD 2>&1 | ForEach-Object { $_.ToString() }) -join ''

$environment = [ordered]@{
    captured_at               = (Get-Date).ToString('o')
    source_commit             = $sourceCommit.Trim()
    operating_system          = $operatingSystem.Caption
    operating_system_version  = $operatingSystem.Version
    operating_system_arch     = $operatingSystem.OSArchitecture
    processor                 = $processor.Name.Trim()
    physical_cores            = $processor.NumberOfCores
    logical_processors        = $processor.NumberOfLogicalProcessors
    visible_memory_gib        = [math]::Round($operatingSystem.TotalVisibleMemorySize / 1MB, 1)
    power_plan                = $powerPlan.Trim()
    go_version                = $goVersion.Trim()
    goos                      = $goEnvironment[0]
    goarch                    = $goEnvironment[1]
    cgo_enabled               = $goEnvironment[2]
    official_module           = $officialModule
    maintained_module         = $maintainedModule
    sec_rx_prefilter          = 'Off'
    benchtime                 = $Benchtime
    repeats                   = $Count
    processor_counts          = $ProcessorCounts
    benchmark_pattern         = $benchmarkPattern
}
Write-Utf8Text -Path (Join-Path $outputRoot 'environment.json') -Text (($environment | ConvertTo-Json -Depth 5) + "`n")

$variants = @(
    [pscustomobject]@{ Name = 'official'; Source = $officialSource },
    [pscustomobject]@{ Name = 'maintained'; Source = $maintainedSource }
)
$detectionChecks = @()
$benchmarkRows = @()
$processorArgument = $ProcessorCounts -join ','

foreach ($variant in $variants) {
    Write-Host "Running detection check for $($variant.Name)"
    $checkOutput = Join-Path $outputRoot "$($variant.Name)-detection-check.txt"
    $check = Invoke-CapturedNative -FilePath 'go' -Arguments @(
        'test', './lib/edgecore', '-run', $goldenPattern, '-count=1', '-v'
    ) -WorkingDirectory $variant.Source -OutputPath $checkOutput
    $detectionChecks += [pscustomobject]@{
        variant   = $variant.Name
        exit_code = $check.ExitCode
        passed    = $check.ExitCode -eq 0
        raw_output = [System.IO.Path]::GetFileName($checkOutput)
    }

    Write-Host "Running capacity benchmark for $($variant.Name)"
    $benchmarkOutput = Join-Path $outputRoot "$($variant.Name)-benchmark.txt"
    $benchmark = Invoke-RequiredNative -FilePath 'go' -Arguments @(
        'test', './lib/edgecore', '-run', '^$', '-bench', $benchmarkPattern,
        "-benchtime=$Benchtime", "-count=$Count", "-cpu=$processorArgument"
    ) -WorkingDirectory $variant.Source -OutputPath $benchmarkOutput
    $benchmarkRows += Convert-BenchmarkLines -Variant $variant.Name -Lines $benchmark.Lines -ConfiguredProcessors $ProcessorCounts
}

$medians = Get-BenchmarkMedians -Rows $benchmarkRows -ExpectedCount $Count
$comparisons = @()
foreach ($official in @($medians | Where-Object { $_.variant -eq 'official' })) {
    $candidate = @($medians | Where-Object {
        $_.variant -eq 'maintained' -and $_.benchmark -eq $official.benchmark -and $_.processors -eq $official.processors
    })
    if ($candidate.Count -ne 1) {
        throw "maintained median missing for $($official.benchmark) with $($official.processors) processors"
    }
    $maintained = $candidate[0]
    $comparisons += [pscustomobject]@{
        benchmark                              = $official.benchmark
        processors                             = $official.processors
        official_median_ns_per_operation       = $official.median_ns_per_operation
        maintained_median_ns_per_operation     = $maintained.median_ns_per_operation
        ns_per_operation_change_percent        = Get-PercentChange -Before $official.median_ns_per_operation -After $maintained.median_ns_per_operation
        official_requests_per_second           = $official.requests_per_second
        maintained_requests_per_second         = $maintained.requests_per_second
        requests_per_second_change_percent     = Get-PercentChange -Before $official.requests_per_second -After $maintained.requests_per_second
        official_bytes_per_operation           = $official.median_bytes_per_operation
        maintained_bytes_per_operation         = $maintained.median_bytes_per_operation
        bytes_per_operation_change_percent     = Get-PercentChange -Before $official.median_bytes_per_operation -After $maintained.median_bytes_per_operation
        official_allocations_per_request       = $official.median_allocations_per_request
        maintained_allocations_per_request     = $maintained.median_allocations_per_request
        allocations_per_request_change_percent = Get-PercentChange -Before $official.median_allocations_per_request -After $maintained.median_allocations_per_request
    }
}

$benchmarkRows | Export-Csv -NoTypeInformation -Encoding utf8 -Path (Join-Path $outputRoot 'raw-results.csv')
$medians | Export-Csv -NoTypeInformation -Encoding utf8 -Path (Join-Path $outputRoot 'median-results.csv')
$comparisons | Export-Csv -NoTypeInformation -Encoding utf8 -Path (Join-Path $outputRoot 'comparison.csv')
Write-Utf8Text -Path (Join-Path $outputRoot 'median-results.json') -Text (($medians | ConvertTo-Json -Depth 5) + "`n")
Write-Utf8Text -Path (Join-Path $outputRoot 'comparison.json') -Text (($comparisons | ConvertTo-Json -Depth 5) + "`n")
Write-Utf8Text -Path (Join-Path $outputRoot 'detection-checks.json') -Text (($detectionChecks | ConvertTo-Json -Depth 5) + "`n")

$smallOrMixed = @($comparisons | Where-Object {
    $_.benchmark -match '/(read_no_body|sql_injection_query|json_1_kib|json_4_kib|mixed_)'
})
$smallOrMixedRegressions = @($smallOrMixed | Where-Object { $_.requests_per_second_change_percent -lt -10 })
$largeProductionShapes = @($comparisons | Where-Object {
    $_.benchmark -match '/(simple|natural_text|base64|binary)_64_kib$'
})
$largeProductionImprovements = @($largeProductionShapes | Where-Object { $_.requests_per_second_change_percent -ge 20 })
$detectionEquivalent = @($detectionChecks | Where-Object { -not $_.passed }).Count -eq 0
$smallAndMixedWithinBudget = $smallOrMixed.Count -gt 0 -and $smallOrMixedRegressions.Count -eq 0
$largeProductionImproved = $largeProductionImprovements.Count -gt 0
$accepted = $detectionEquivalent -and $smallAndMixedWithinBudget -and $largeProductionImproved

$acceptance = [ordered]@{
    detection_equivalent                 = $detectionEquivalent
    small_and_mixed_regression_limit     = '-10% requests per second'
    small_and_mixed_within_budget        = $smallAndMixedWithinBudget
    small_and_mixed_regressions          = $smallOrMixedRegressions
    large_production_improvement_target  = '+20% requests per second'
    large_production_improved            = $largeProductionImproved
    qualifying_large_production_results  = $largeProductionImprovements
    accepted                             = $accepted
}
Write-Utf8Text -Path (Join-Path $outputRoot 'acceptance.json') -Text (($acceptance | ConvertTo-Json -Depth 8) + "`n")

Write-Host "Benchmark output: $outputRoot"
$comparisons |
    Select-Object benchmark, processors,
        @{n = 'official_rps'; e = { [math]::Round($_.official_requests_per_second, 2) } },
        @{n = 'maintained_rps'; e = { [math]::Round($_.maintained_requests_per_second, 2) } },
        @{n = 'rps_change_percent'; e = { [math]::Round($_.requests_per_second_change_percent, 2) } } |
    Format-Table -AutoSize

if (-not $accepted) {
    Write-Error "Coraza replacement did not satisfy acceptance gates; see $outputRoot"
    exit 1
}
