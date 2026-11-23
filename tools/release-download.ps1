#requires -Version 5.1
# Internal bounded transport. Dot-sourcing defines functions only; no network request.
function Test-RCPositiveInteger {
    param($Value)
    return ($Value -is [int] -or $Value -is [long]) -and $Value -gt 0
}

function Assert-RCWebLocation {
    param([string]$Uri, [switch]$Metadata, [switch]$InitialAsset)
    $parsed = $null
    if (-not [uri]::TryCreate($Uri, [UriKind]::Absolute, [ref]$parsed) -or
        $parsed.Scheme -cne 'https' -or $parsed.Port -ne 443 -or $parsed.UserInfo -or $parsed.Fragment) {
        throw 'Release request location is not an allowed official HTTPS URL.'
    }
    if ($Metadata) {
        if ($parsed.Host -cne 'api.github.com' -or $parsed.Query -or
            $parsed.AbsolutePath -cnotmatch '\A/repos/(MetaCubeX/mihomo|Zephyruso/zashboard|MetaCubeX/meta-rules-dat)/(releases/(tags/[A-Za-z0-9._-]+|[1-9][0-9]*)|git/ref/tags/[A-Za-z0-9._-]+|git/tags/[0-9a-f]{40})\z') {
            throw 'Metadata must use a fixed official GitHub API location.'
        }
    } elseif ($InitialAsset) {
        if ($parsed.Host -cne 'github.com' -or $parsed.Query -or
            $parsed.AbsolutePath -cnotmatch '\A/(MetaCubeX/mihomo|Zephyruso/zashboard|MetaCubeX/meta-rules-dat)/releases/download/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+\z') {
            throw 'Asset must use a fixed official GitHub release location.'
        }
    } elseif ($parsed.Host -cnotin @('github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com', 'github-releases.githubusercontent.com')) {
        throw 'Release redirect is not an allowed official GitHub HTTPS object service.'
    } elseif ($parsed.Host -ceq 'github.com') {
        Assert-RCWebLocation $Uri -InitialAsset
    }
}

function Get-RCRequestHeaders {
    param([string]$Uri, [string]$Destination = '')
    $headers = @{ 'User-Agent' = 'Mihomo-Lite-release-components/1'; 'Accept' = 'application/vnd.github+json' }
    # The CI token is read only from the environment. Never send it to release
    # browser/object hosts or follow metadata redirects with it.
    if (-not $Destination -and ([uri]$Uri).Host -ceq 'api.github.com' -and $env:GITHUB_TOKEN) {
        Assert-RCWebLocation $Uri -Metadata
        if ($env:GITHUB_TOKEN -cnotmatch '\A[A-Za-z0-9_.-]+\z') { throw 'Invalid GitHub API token format.' }
        $headers['Authorization'] = 'Bearer ' + $env:GITHUB_TOKEN
    }
    return $headers
}

function Invoke-RCTransport {
    [CmdletBinding()]
    param([string]$Uri, [long]$MaxBytes, [string]$Destination = '')
    $response = $null
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        $request = [Net.HttpWebRequest]::Create($Uri)
        $request.AllowAutoRedirect = $false
        $request.Timeout = 60000
        $request.ReadWriteTimeout = 60000
        $headers = Get-RCRequestHeaders $Uri $Destination
        $request.UserAgent = $headers['User-Agent']
        $request.Accept = $headers['Accept']
        if ($headers.ContainsKey('Authorization')) { $request.Headers['Authorization'] = $headers['Authorization'] }
        try { $response = $request.GetResponse() } catch [Net.WebException] {
            $response = $_.Exception.Response
            if ($null -eq $response) { throw 'Transport failed.' }
        }
        $status = [int]$response.StatusCode
        if ($status -ne 200) {
            return [pscustomobject]@{ statusCode = $status; location = [string]$response.Headers['Location']; content = $null }
        }
        if ($response.ContentLength -gt $MaxBytes) { throw 'Response exceeds size bound.' }
        $inputStream = $response.GetResponseStream()
        $outputStream = $null
        try {
            if ($Destination) {
                $outputStream = [IO.File]::Open($Destination, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
            } else { $outputStream = [IO.MemoryStream]::new() }
            $buffer = New-Object byte[] 65536
            [long]$total = 0
            while (($count = $inputStream.Read($buffer, 0, $buffer.Length)) -gt 0) {
                $total += $count
                if ($total -gt $MaxBytes) { throw 'Response exceeds size bound.' }
                $outputStream.Write($buffer, 0, $count)
            }
            if ($total -le 0) { throw 'Empty response.' }
            $content = $null
            if (-not $Destination) { $content = $outputStream.ToArray() }
            return [pscustomobject]@{ statusCode = 200; location = ''; content = $content }
        } finally {
            if ($outputStream) { $outputStream.Dispose() }
            $inputStream.Dispose()
        }
    } catch {
        # A redirect URL may include a token: never echo the original exception.
        throw 'Official release transport failed or exceeded its size limit.'
    } finally { if ($response) { $response.Dispose() } }
}

function Get-RCJson {
    param([string]$Uri)
    Assert-RCWebLocation $Uri -Metadata
    try {
        $response = Invoke-RCTransport -Uri $Uri -MaxBytes 4194304
        if ($response.statusCode -ne 200) { throw 'Unexpected metadata status.' }
        return (ConvertFrom-Json -InputObject ([Text.Encoding]::UTF8.GetString($response.content)) -ErrorAction Stop)
    } catch { throw 'Unable to read valid official release metadata.' }
}

function Save-RCAsset {
    param($Asset, [string]$Destination)
    Assert-RCWebLocation $Asset.downloadUrl -InitialAsset
    $uri = $Asset.downloadUrl
    for ($hop = 0; $hop -le 5; $hop++) {
        Assert-RCWebLocation $uri -InitialAsset:($hop -eq 0)
        try { $response = Invoke-RCTransport -Uri $uri -MaxBytes $Asset.size -Destination $Destination }
        catch { throw 'Unable to download bounded official release asset.' }
        if ($response.statusCode -eq 200) { return }
        if ($response.statusCode -notin @(301, 302, 303, 307, 308) -or $hop -eq 5 -or -not $response.location) {
            throw 'Official release asset returned an invalid status or too many redirects.'
        }
        Assert-RCWebLocation $response.location
        if (([uri]$response.location).Host -ceq 'github.com' -and $response.location -cne $Asset.downloadUrl) {
            throw 'Official asset redirect changed its fixed download location.'
        }
        $uri = $response.location
    }
}
