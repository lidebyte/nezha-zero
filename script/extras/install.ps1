#Get server and key; remaining args (e.g. --tls --disable-command-execute) are forwarded
param(
    [Parameter(Mandatory = $true, Position = 0)]
    $server,
    [Parameter(Mandatory = $true, Position = 1)]
    $key,
    [Parameter(ValueFromRemainingArguments = $true)]
    $extra
)
# Download latest release from github
if($PSVersionTable.PSVersion.Major -lt 5){
    Write-Host "Require PS >= 5,your PSVersion:"$PSVersionTable.PSVersion.Major -BackgroundColor DarkGreen -ForegroundColor White
    Write-Host "Refer to the community article and install manually! https://nyko.me/2020/12/13/nezha-windows-client.html" -BackgroundColor DarkRed -ForegroundColor Green
    exit
}
$agentrepo = "railzen/nezha-zero"
#  x86 or x64 or arm64
if ([System.Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
        $file = "nezha-agent_windows_arm64.zip"
    } else {
        $file = "nezha-agent_windows_amd64.zip"
    }
}
else {
    $file = "nezha-agent_windows_386.zip"
}
$agentreleases = "https://api.github.com/repos/$agentrepo/releases/latest"
#TLS/SSL
Write-Host "Determining latest nezha release" -BackgroundColor DarkGreen -ForegroundColor White
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
# releases/latest excludes prereleases and drafts; exit on failure instead of falling back to sources that may return a prerelease tag
$agenttag = (Invoke-WebRequest -Uri $agentreleases -UseBasicParsing | ConvertFrom-Json).tag_name
if ($agenttag -notmatch '^v\d+\.\d+\.\d+$') {
    Write-Host "Failed to determine agent version, please check if the network can reach $agentreleases" -BackgroundColor DarkRed -ForegroundColor White
    exit 1
}
#重复运行自动更新
# Uninstall only after the version is resolved, so a failed lookup keeps the existing agent
if (Test-Path "C:\nezha\nezha-agent.exe") {
    Write-Host "Nezha monitoring already exists, delete and reinstall" -BackgroundColor DarkGreen -ForegroundColor White
    C:\nezha\nezha-agent.exe service uninstall
    Remove-Item "C:\nezha" -Recurse
}
#Region判断
#$ipapi = ""
$region = "Unknown"
#foreach ($url in ("https://dash.cloudflare.com/cdn-cgi/trace","https://developers.cloudflare.com/cdn-cgi/trace","https://1.0.0.1/cdn-cgi/trace")) {
#    try {
#        $ipapi = Invoke-RestMethod -Uri $url -TimeoutSec 5 -UseBasicParsing
#        if ($ipapi -match "loc=(\w+)" ) {
#            $region = $Matches[1]
#            break
#        }
#    }
#    catch {
#        Write-Host "Error occurred while querying $url : $_"
#    }
#}
#echo $ipapi

$download = "https://github.com/$agentrepo/releases/download/$agenttag/$file"
Write-Host "Location:$region,connect directly!" -BackgroundColor DarkRed -ForegroundColor Green

echo $download
# Use a private per-run temp dir and only ever delete that dir
$tmpdir = Join-Path $env:TEMP ("nezha-" + [guid]::NewGuid().ToString())
New-Item -Path $tmpdir -ItemType Directory | Out-Null
try {
    Invoke-WebRequest $download -OutFile "$tmpdir\nezha.zip"
#解压
    Expand-Archive "$tmpdir\nezha.zip" -DestinationPath "$tmpdir\extract" -Force
    if (!(Test-Path "C:\nezha")) { New-Item -Path "C:\nezha" -type directory }
    icacls "C:\nezha" /inheritance:r /grant:r "*S-1-5-32-544:(OI)(CI)F" "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-545:(OI)(CI)RX" | Out-Null
#整理文件
    Copy-Item -Path "$tmpdir\extract\nezha-agent.exe" -Destination "C:\nezha\nezha-agent.exe"
} finally {
#清理垃圾
    Remove-Item $tmpdir -Recurse -Force -ErrorAction SilentlyContinue
}
#安装部分
C:\nezha\nezha-agent.exe service install -s $server -p $key @extra
#enjoy
Write-Host "Enjoy It!" -BackgroundColor DarkGreen -ForegroundColor Red
