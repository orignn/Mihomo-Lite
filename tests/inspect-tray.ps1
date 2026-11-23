<#
.SYNOPSIS
Capture only a PID-bound shell notification icon and verify its UIA/hover text.
.DESCRIPTION
Does not change settings, invoke the application's menu, or close user windows.
Only the system hidden-icons overflow button may be invoked. Cursor and overflow
state are restored in finally. The PNG is a bounded icon patch, not a full-screen
capture. Actual RGB samples are reported without inferring gray/green state.
Existing PNG/JSON outputs are never overwritten. JSON is also returned on stdout.
.EXAMPLE
.\tests\inspect-tray.ps1 -TargetProcessId 1234 -ImagePath .cache\tray-idle.png -ExpectedTooltip 'Mihomo Lite - 未连接'
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateRange(1, 2147483647)][int]$TargetProcessId,
    [Parameter(Mandatory = $true)][ValidateNotNullOrEmpty()][string]$ImagePath,
    [Parameter(Mandatory = $true)][ValidateNotNullOrEmpty()][string]$ExpectedTooltip
)

$ErrorActionPreference = 'Stop'
$result = [ordered]@{
    Supported = $false
    Reason = $null
    TargetProcessId = $TargetProcessId
    TargetWindow = $null
    ExpectedTooltip = $ExpectedTooltip
    ObservedTooltip = $null
    TooltipSource = $null
    TooltipVerified = $false
    NativeTooltipObserved = $null
    NativeTooltipVerified = $false
    TooltipOwnerProcessId = $null
    TooltipWindow = $null
    UIAutomationAvailable = $false
    UIAutomationAssemblies = @()
    ImagePath = $ImagePath
    JsonPath = $null
    ScreenshotCaptured = $false
    ImageCapturedAtUtc = $null
    FinishedAtUtc = $null
    IdentifierSize = $null
    ShellHRESULT = $null
    RawIconRect = $null
    ImageCapturedBeforeHover = $true
    IconRect = $null
    CropRect = $null
    PixelStatistics = $null
    ColorClassification = 'Not inferred; compare actual PNG/RGB samples.'
    ShellHitClass = $null
    OriginalCursor = $null
    CursorRestored = $false
    OriginalOverflowOpen = $null
    OverflowRestored = $false
    OverflowChangedByScript = $false
    DpiContextRestored = $false
    StartedAtUtc = [DateTime]::UtcNow.ToString('o')
    Diagnostics = @()
}
$nativeReady = $false
$oldDpiContext = [IntPtr]::Zero
$originalCursor = $null
$overflowButton = $null
$overflowTouched = $false
$writeSidecar = $false

function Resolve-SystemUIAssembly([string]$AssemblyName) {
    $windowsDir = $env:WINDIR
    if ([string]::IsNullOrEmpty($windowsDir)) { throw 'WINDIR is unavailable.' }
    $frameworks = @('Framework64', 'Framework')
    if ([IntPtr]::Size -eq 4) { $frameworks = @('Framework', 'Framework64') }
    foreach ($framework in $frameworks) {
        $base = Join-Path $windowsDir ('Microsoft.NET\' + $framework + '\v4.0.30319')
        foreach ($directory in @((Join-Path $base 'WPF'), $base)) {
            $candidate = Join-Path $directory ($AssemblyName + '.dll')
            if ([IO.File]::Exists($candidate)) { return [IO.Path]::GetFullPath($candidate) }
        }
    }
    foreach ($gac in @('Microsoft.NET\assembly\GAC_MSIL', 'assembly\GAC_MSIL')) {
        $base = Join-Path (Join-Path $windowsDir $gac) $AssemblyName
        if ([IO.Directory]::Exists($base)) {
            $files = @(Get-ChildItem -LiteralPath $base -Filter ($AssemblyName + '.dll') -Recurse -File | Sort-Object FullName -Descending)
            if ($files.Count -gt 0) { return $files[0].FullName }
        }
    }
    throw ('Existing system assembly not found: ' + $AssemblyName)
}

function Get-UIAName($Element) {
    if ($null -eq $Element) { return '' }
    $value = $Element.GetCurrentPropertyValue([System.Windows.Automation.AutomationElement]::NameProperty, $true)
    if ([object]::ReferenceEquals($value, [System.Windows.Automation.AutomationElement]::NotSupported)) { return '' }
    return [string]$value
}

function Find-OverflowButton {
    if (-not $result.UIAutomationAvailable) { return $null }
    foreach ($handle in [MihomoLiteTrayInspectNative]::TaskbarWindows()) {
        try {
            $root = [System.Windows.Automation.AutomationElement]::FromHandle($handle)
            $buttons = $root.FindAll([System.Windows.Automation.TreeScope]::Descendants,
                [System.Windows.Automation.PropertyCondition]::new(
                    [System.Windows.Automation.AutomationElement]::ControlTypeProperty,
                    [System.Windows.Automation.ControlType]::Button))
            foreach ($button in $buttons) {
                if ((Get-UIAName $button) -in @('显示隐藏的图标', '显示隐藏图标', 'Show hidden icons', 'Hide hidden icons', '隐藏图标')) {
                    return $button
                }
            }
        } catch {
            $result.Diagnostics += ('Taskbar UIA lookup: ' + $_.Exception.Message)
        }
    }
    return $null
}

function Invoke-OverflowButton($Button) {
    if ($null -eq $Button) { throw 'Hidden-icons button could not be resolved through UIA.' }
    $pattern = $null
    if ($Button.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern, [ref]$pattern)) {
        $pattern.Invoke()
        return
    }
    throw 'Hidden-icons button does not provide a reliable UIA InvokePattern.'
}
function Test-IconRect($Rect, [int]$Status) {
    if ($Status -ne 0 -or $Rect.Width -le 0 -or $Rect.Height -le 0 -or $Rect.Width -gt 96 -or $Rect.Height -gt 96) { return $false }
    if (-not [MihomoLiteTrayInspectNative]::WithinDesktop($Rect)) { return $false }
    return [MihomoLiteTrayInspectNative]::IsShellRegionAt($Rect.Left + [int]($Rect.Width / 2), $Rect.Top + [int]($Rect.Height / 2))
}

function Normalize-Tooltip([string]$Text) {
    if ($null -eq $Text) { return '' }
    return ([regex]::Replace($Text, '\s+', ' ')).Trim()
}

function Read-TooltipEvidence([int]$X, [int]$Y, [uint32]$ShellProcessId) {
    $expected = Normalize-Tooltip $ExpectedTooltip
    $evidence = [ordered]@{ Verified = $false; Observed = $null; Source = $null }
    $watch = [Diagnostics.Stopwatch]::StartNew()
    while ($watch.ElapsedMilliseconds -lt 3000) {
        if ($result.UIAutomationAvailable) {
            try {
                $point = [System.Windows.Point]::new([double]$X, [double]$Y)
                $element = [System.Windows.Automation.AutomationElement]::FromPoint($point)
                $name = Get-UIAName $element
                if ((Normalize-Tooltip $name) -eq $expected) {
                    return [pscustomobject]@{ Verified = $true; Observed = $name; Source = 'UIA notification-icon Name at the PID-bound shell rectangle' }
                }
                if (-not [string]::IsNullOrWhiteSpace($name)) { $evidence.Observed = $name; $evidence.Source = 'UIA Name at icon center (did not match)' }
            } catch {
                if ($watch.ElapsedMilliseconds -lt 200) { $result.Diagnostics += ('UIA FromPoint: ' + $_.Exception.Message) }
            }
        }
        # Inspect only visible, nearby tooltip-class windows owned by the shell,
        # not unrelated application windows or the entire automation tree.
        foreach ($handle in [MihomoLiteTrayInspectNative]::NearbyTooltips($X, $Y, $ShellProcessId)) {
            $nativeText = [MihomoLiteTrayInspectNative]::WindowText($handle)
            if (-not [string]::IsNullOrWhiteSpace($nativeText)) {
                $result.NativeTooltipObserved = $nativeText
                $result.NativeTooltipVerified = ((Normalize-Tooltip $nativeText) -ceq $expected)
                $evidence.Observed = $nativeText
                $evidence.Source = 'Nearby shell tooltip WM_GETTEXT'
                if ($result.NativeTooltipVerified) {
                    $result.TooltipOwnerProcessId = $ShellProcessId
                    $result.TooltipWindow = '0x' + $handle.ToInt64().ToString('x')
                    return [pscustomobject]@{ Verified = $true; Observed = $nativeText; Source = 'Bounded WM_GETTEXT of nearby shell-owned hover tooltip, normalized exact match' }
                }
            }
            if (-not $result.UIAutomationAvailable) { continue }
            try {
                $element = [System.Windows.Automation.AutomationElement]::FromHandle($handle)
                $names = @(Get-UIAName $element)
                $texts = $element.FindAll([System.Windows.Automation.TreeScope]::Descendants,
                    [System.Windows.Automation.PropertyCondition]::new(
                        [System.Windows.Automation.AutomationElement]::ControlTypeProperty,
                        [System.Windows.Automation.ControlType]::Text))
                $limit = [Math]::Min($texts.Count, 10)
                for ($index = 0; $index -lt $limit; $index++) { $names += (Get-UIAName $texts[$index]) }
                foreach ($name in @($names + [string]::Join(' ', [string[]]$names))) {
                    if ((Normalize-Tooltip $name) -eq $expected) {
                        return [pscustomobject]@{ Verified = $true; Observed = $name; Source = ('UIA hover tooltip: ' + [MihomoLiteTrayInspectNative]::WindowClass($handle)) }
                    }
                }
            } catch {
                if ($watch.ElapsedMilliseconds -lt 200) { $result.Diagnostics += ('UIA tooltip provider: ' + $_.Exception.Message) }
            }
        }
        Start-Sleep -Milliseconds 150
    }
    return [pscustomobject]$evidence
}

function Save-IconPng([byte[]]$Pixels, [int]$Width, [int]$Height, [string]$Path) {
    Add-Type -AssemblyName System.Drawing
    $bitmap = [System.Drawing.Bitmap]::new($Width, $Height, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    try {
        $locked = $bitmap.LockBits([System.Drawing.Rectangle]::new(0, 0, $Width, $Height),
            [System.Drawing.Imaging.ImageLockMode]::WriteOnly, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
        try {
            for ($row = 0; $row -lt $Height; $row++) {
                [Runtime.InteropServices.Marshal]::Copy($Pixels, $row * $Width * 4,
                    [IntPtr]::Add($locked.Scan0, $row * $locked.Stride), $Width * 4)
            }
        } finally { $bitmap.UnlockBits($locked) }
        $bitmap.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    } finally { $bitmap.Dispose() }
}

function Get-PixelStatistics([byte[]]$Pixels, [int]$Width, [int]$Height) {
    [long]$sumR = 0; [long]$sumG = 0; [long]$sumB = 0
    $count = $Width * $Height
    for ($offset = 0; $offset -lt $Pixels.Length; $offset += 4) {
        $sumB += $Pixels[$offset]; $sumG += $Pixels[$offset + 1]; $sumR += $Pixels[$offset + 2]
    }
    $centerX = [int][Math]::Floor($Width / 2); $centerY = [int][Math]::Floor($Height / 2)
    $samples = @()
    for ($dy = -1; $dy -le 1; $dy++) {
        for ($dx = -1; $dx -le 1; $dx++) {
            $x = [Math]::Max(0, [Math]::Min($Width - 1, $centerX + $dx))
            $y = [Math]::Max(0, [Math]::Min($Height - 1, $centerY + $dy))
            $offset = ($y * $Width + $x) * 4
            $samples += [ordered]@{ X = $x; Y = $y; R = [int]$Pixels[$offset + 2]; G = [int]$Pixels[$offset + 1]; B = [int]$Pixels[$offset]; A = 255 }
        }
    }
    return [ordered]@{
        Width = $Width; Height = $Height; PixelCount = $count
        MeanRGB = [ordered]@{ R = [Math]::Round($sumR / $count, 3); G = [Math]::Round($sumG / $count, 3); B = [Math]::Round($sumB / $count, 3) }
        Center3x3 = $samples
        Note = 'Raw screen samples only; backgrounds and antialiasing are included. No connection/color state inferred.'
    }
}

try {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Windows is required.' }
    $absoluteImagePath = [IO.Path]::GetFullPath($ImagePath)
    if ([IO.Path]::GetExtension($absoluteImagePath) -ine '.png') { throw 'ImagePath must name a PNG file.' }
    $sidecarPath = $absoluteImagePath + '.json'
    $result.ImagePath = $absoluteImagePath; $result.JsonPath = $sidecarPath
    if ([IO.File]::Exists($absoluteImagePath) -or [IO.File]::Exists($sidecarPath)) { throw 'Refusing to overwrite an existing PNG or JSON output; choose a new ImagePath.' }
    [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($absoluteImagePath))
    $writeSidecar = $true
    if ($null -eq ('MihomoLiteTrayInspectNative' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;

[StructLayout(LayoutKind.Sequential)]
public struct MihomoLiteTrayInspectPoint { public int X, Y; }
[StructLayout(LayoutKind.Sequential)]
public struct MihomoLiteTrayInspectRect {
    public int Left, Top, Right, Bottom;
    public int Width { get { return Right - Left; } }
    public int Height { get { return Bottom - Top; } }
}
public static class MihomoLiteTrayInspectNative {
    [StructLayout(LayoutKind.Sequential)]
    struct IconIdentifier { public uint Size; public IntPtr Wnd; public uint ID; public Guid Guid; }
    [StructLayout(LayoutKind.Sequential)]
    struct BitmapHeader { public uint Size; public int Width, Height; public ushort Planes, Bits; public uint Compression, SizeImage; public int XPixels, YPixels; public uint Used, Important; }
    [StructLayout(LayoutKind.Sequential)]
    struct BitmapInfo { public BitmapHeader Header; public uint Color; }
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern IntPtr FindWindowEx(IntPtr parent, IntPtr after, string cls, string name);
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetClassName(IntPtr window, StringBuilder text, int count);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetWindowText(IntPtr window, StringBuilder text, int count);
    [DllImport("user32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr SendMessageTimeout(IntPtr window, uint message, UIntPtr wParam, StringBuilder lParam, uint flags, uint timeout, out UIntPtr result);
    [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr window);
    [DllImport("user32.dll")] static extern bool GetWindowRect(IntPtr window, out MihomoLiteTrayInspectRect rect);
    [DllImport("user32.dll")] static extern bool GetCursorPos(out MihomoLiteTrayInspectPoint point);
    [DllImport("user32.dll")] static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] static extern IntPtr WindowFromPoint(MihomoLiteTrayInspectPoint point);
    [DllImport("user32.dll")] static extern IntPtr GetParent(IntPtr window);
    [DllImport("user32.dll")] static extern int GetSystemMetrics(int index);
    [DllImport("user32.dll")] static extern IntPtr SetThreadDpiAwarenessContext(IntPtr context);
    [DllImport("shell32.dll")] static extern int Shell_NotifyIconGetRect(ref IconIdentifier identifier, out MihomoLiteTrayInspectRect rect);
    [DllImport("user32.dll")] static extern IntPtr GetDC(IntPtr window);
    [DllImport("user32.dll")] static extern int ReleaseDC(IntPtr window, IntPtr dc);
    [DllImport("gdi32.dll")] static extern IntPtr CreateCompatibleDC(IntPtr dc);
    [DllImport("gdi32.dll")] static extern bool DeleteDC(IntPtr dc);
    [DllImport("gdi32.dll")] static extern IntPtr SelectObject(IntPtr dc, IntPtr item);
    [DllImport("gdi32.dll")] static extern bool DeleteObject(IntPtr item);
    [DllImport("gdi32.dll")] static extern IntPtr CreateDIBSection(IntPtr dc, ref BitmapInfo info, uint usage, out IntPtr bits, IntPtr section, uint offset);
    [DllImport("gdi32.dll", SetLastError=true)] static extern bool BitBlt(IntPtr destination, int dx, int dy, int width, int height, IntPtr source, int sx, int sy, uint operation);

    public static uint IdentifierSize { get { return (uint)Marshal.SizeOf(typeof(IconIdentifier)); } }
    public static string WindowClass(IntPtr window) { var text = new StringBuilder(256); GetClassName(window,text,text.Capacity); return text.ToString(); }
    public static string WindowText(IntPtr window) {
        var text = new StringBuilder(1024); UIntPtr result;
        // Cross-process tooltip controls do not reliably expose GetWindowText.
        // WM_GETTEXT is a read-only system message with bounded blocking time.
        if(SendMessageTimeout(window,0x000d,new UIntPtr((uint)text.Capacity),text,0x0003,500,out result)!=IntPtr.Zero && text.Length>0) return text.ToString();
        GetWindowText(window,text,text.Capacity); return text.ToString();
    }
    public static uint Owner(IntPtr window) { uint id; GetWindowThreadProcessId(window,out id); return id; }
    static IntPtr[] ClassWindows(string cls, bool visibleOnly) {
        var result = new List<IntPtr>(); IntPtr current = IntPtr.Zero;
        while ((current=FindWindowEx(IntPtr.Zero,current,cls,null)) != IntPtr.Zero) {
            if (!visibleOnly || IsWindowVisible(current)) result.Add(current);
        }
        return result.ToArray();
    }
    public static IntPtr Systray(uint processId) { foreach(var window in ClassWindows("SystrayClass",false)) if (Owner(window)==processId) return window; return IntPtr.Zero; }
    public static IntPtr[] TaskbarWindows() { var windows=new List<IntPtr>(); windows.AddRange(ClassWindows("Shell_TrayWnd",true)); windows.AddRange(ClassWindows("Shell_SecondaryTrayWnd",true)); return windows.ToArray(); }
    public static bool OverflowOpen() { return ClassWindows("NotifyIconOverflowWindow",true).Length>0 || ClassWindows("TopLevelWindowForOverflowXamlIsland",true).Length>0; }
    public static bool OverflowClassKnown() { return ClassWindows("NotifyIconOverflowWindow",false).Length>0 || ClassWindows("TopLevelWindowForOverflowXamlIsland",false).Length>0; }
    public static MihomoLiteTrayInspectRect IconRect(IntPtr window, out int status) { var id=new IconIdentifier { Size=(uint)Marshal.SizeOf(typeof(IconIdentifier)), Wnd=window, ID=100 }; MihomoLiteTrayInspectRect rect; status=Shell_NotifyIconGetRect(ref id,out rect); return rect; }
    public static MihomoLiteTrayInspectPoint Cursor() { MihomoLiteTrayInspectPoint point; if (!GetCursorPos(out point)) throw new Win32Exception(); return point; }
    public static bool MoveCursor(int x,int y) { return SetCursorPos(x,y); }
    public static IntPtr EnterDpiContext() { try { return SetThreadDpiAwarenessContext(new IntPtr(-4)); } catch(EntryPointNotFoundException) { return IntPtr.Zero; } }
    public static bool RestoreDpiContext(IntPtr previous) { if(previous==IntPtr.Zero) return true; return SetThreadDpiAwarenessContext(previous)!=IntPtr.Zero; }
    public static bool WithinDesktop(MihomoLiteTrayInspectRect rect) { int x=GetSystemMetrics(76),y=GetSystemMetrics(77); return rect.Left>=x && rect.Top>=y && rect.Right<=x+GetSystemMetrics(78) && rect.Bottom<=y+GetSystemMetrics(79); }
    public static string HitClass(int x,int y) { return WindowClass(WindowFromPoint(new MihomoLiteTrayInspectPoint {X=x,Y=y})); }
    public static uint HitOwner(int x,int y) { return Owner(WindowFromPoint(new MihomoLiteTrayInspectPoint {X=x,Y=y})); }
    public static bool IsShellRegionAt(int x,int y) {
        IntPtr window=WindowFromPoint(new MihomoLiteTrayInspectPoint {X=x,Y=y});
        for(int depth=0; window!=IntPtr.Zero && depth<12; depth++,window=GetParent(window)) {
            string cls=WindowClass(window);
            if(cls=="Shell_TrayWnd" || cls=="Shell_SecondaryTrayWnd" || cls=="NotifyIconOverflowWindow" || cls=="TopLevelWindowForOverflowXamlIsland" || cls=="XamlExplorerHostIslandWindow") return true;
        }
        return false;
    }
    public static IntPtr[] NearbyTooltips(int x,int y,uint owner) {
        var result=new List<IntPtr>();
        foreach(string cls in new[]{"tooltips_class32","Xaml_WindowedPopupClass"}) foreach(var window in ClassWindows(cls,true)) {
            MihomoLiteTrayInspectRect rect;
            if(Owner(window)==owner && GetWindowRect(window,out rect) && rect.Width>0 && rect.Width<=700 && rect.Height>0 && rect.Height<=240 && x>=rect.Left-160 && x<=rect.Right+160 && y>=rect.Top-160 && y<=rect.Bottom+160) result.Add(window);
        }
        return result.ToArray();
    }
    public static byte[] CaptureBgra(int x,int y,int width,int height) {
        if(width<1 || height<1 || width>96 || height>96) throw new ArgumentException("Capture is restricted to a small notification icon patch.");
        IntPtr screen=IntPtr.Zero,dc=IntPtr.Zero,bitmap=IntPtr.Zero,previous=IntPtr.Zero,bits=IntPtr.Zero;
        try {
            screen=GetDC(IntPtr.Zero); if(screen==IntPtr.Zero) throw new Win32Exception();
            dc=CreateCompatibleDC(screen); if(dc==IntPtr.Zero) throw new Win32Exception();
            var info=new BitmapInfo { Header=new BitmapHeader { Size=40, Width=width, Height=-height, Planes=1, Bits=32, Compression=0 } };
            bitmap=CreateDIBSection(screen,ref info,0,out bits,IntPtr.Zero,0); if(bitmap==IntPtr.Zero || bits==IntPtr.Zero) throw new Win32Exception();
            previous=SelectObject(dc,bitmap);
            if(!BitBlt(dc,0,0,width,height,screen,x,y,0x40cc0020)) throw new Win32Exception(Marshal.GetLastWin32Error());
            var pixels=new byte[width*height*4]; Marshal.Copy(bits,pixels,0,pixels.Length);
            for(int index=3;index<pixels.Length;index+=4) pixels[index]=255;
            return pixels;
        } finally { if(previous!=IntPtr.Zero && dc!=IntPtr.Zero) SelectObject(dc,previous); if(bitmap!=IntPtr.Zero) DeleteObject(bitmap); if(dc!=IntPtr.Zero) DeleteDC(dc); if(screen!=IntPtr.Zero) ReleaseDC(IntPtr.Zero,screen); }
    }
}
'@
    }
    $nativeReady = $true
    $result.IdentifierSize = [MihomoLiteTrayInspectNative]::IdentifierSize
    $oldDpiContext = [MihomoLiteTrayInspectNative]::EnterDpiContext()
    $originalCursor = [MihomoLiteTrayInspectNative]::Cursor()
    $result.OriginalCursor = [ordered]@{ X = $originalCursor.X; Y = $originalCursor.Y }
    $result.OriginalOverflowOpen = [MihomoLiteTrayInspectNative]::OverflowOpen()
    $hwnd = [MihomoLiteTrayInspectNative]::Systray([uint32]$TargetProcessId)
    if ($hwnd -eq [IntPtr]::Zero) { throw 'No PID-owned SystrayClass HWND was found.' }
    $result.TargetWindow = '0x' + $hwnd.ToInt64().ToString('x')

    try {
        foreach ($assembly in @('WindowsBase', 'UIAutomationTypes', 'UIAutomationClient')) {
            $assemblyPath = Resolve-SystemUIAssembly $assembly
            [void][Reflection.Assembly]::LoadFrom($assemblyPath)
            $result.UIAutomationAssemblies += $assemblyPath
        }
        if ($null -eq ('System.Windows.Automation.AutomationElement' -as [type])) { throw 'AutomationElement did not load.' }
        $result.UIAutomationAvailable = $true
    } catch { $result.Diagnostics += ('UIAutomation unavailable: ' + $_.Exception.Message) }
    $overflowButton = Find-OverflowButton
    $status = 0
    $rect = [MihomoLiteTrayInspectNative]::IconRect($hwnd, [ref]$status)
    $result.ShellHRESULT = ('0x{0:x8}' -f $status)
    $result.RawIconRect = [ordered]@{ Left=$rect.Left; Top=$rect.Top; Right=$rect.Right; Bottom=$rect.Bottom }
    if (-not (Test-IconRect $rect $status)) {
        if ($result.OriginalOverflowOpen) { throw 'Icon rectangle is not a verified shell area even with overflow already open.' }
        if (-not [MihomoLiteTrayInspectNative]::OverflowClassKnown()) {
            throw 'Overflow initial state cannot be established reliably; refusing a toggle that might not be restorable.'
        }
        if ($null -eq $overflowButton) { throw 'Icon is hidden/unavailable and the localized overflow button is not accessible through UIA.' }
        $overflowTouched = $true
        Invoke-OverflowButton $overflowButton
        $watch = [Diagnostics.Stopwatch]::StartNew()
        do {
            Start-Sleep -Milliseconds 100
            $rect = [MihomoLiteTrayInspectNative]::IconRect($hwnd, [ref]$status)
            $result.ShellHRESULT = ('0x{0:x8}' -f $status)
            $result.RawIconRect = [ordered]@{ Left=$rect.Left; Top=$rect.Top; Right=$rect.Right; Bottom=$rect.Bottom }
        } while (-not (Test-IconRect $rect $status) -and $watch.ElapsedMilliseconds -lt 3000)
        if (-not (Test-IconRect $rect $status)) { throw 'Shell_NotifyIconGetRect did not yield a visible bounded shell icon after opening overflow.' }
        $result.OverflowChangedByScript = $true
    }
    if ([MihomoLiteTrayInspectNative]::Owner($hwnd) -ne [uint32]$TargetProcessId) { throw 'Target HWND ownership changed; capture aborted.' }
    $result.IconRect = [ordered]@{ Left = $rect.Left; Top = $rect.Top; Right = $rect.Right; Bottom = $rect.Bottom; Width = $rect.Width; Height = $rect.Height; IconId = 100; Source = 'Shell_NotifyIconGetRect' }
    # Capture exactly the actual shell rectangle, with no additional margin.
    $result.CropRect = $result.IconRect
    $pixels = [MihomoLiteTrayInspectNative]::CaptureBgra($rect.Left, $rect.Top, $rect.Width, $rect.Height)
    Save-IconPng $pixels $rect.Width $rect.Height $absoluteImagePath
    $result.ScreenshotCaptured = $true
    $result.ImageCapturedAtUtc = [DateTime]::UtcNow.ToString('o')
    $result.PixelStatistics = Get-PixelStatistics $pixels $rect.Width $rect.Height
    $centerX = $rect.Left + [int][Math]::Floor($rect.Width / 2)
    $centerY = $rect.Top + [int][Math]::Floor($rect.Height / 2)
    $result.ShellHitClass = [MihomoLiteTrayInspectNative]::HitClass($centerX, $centerY)
    $shellProcessId = [MihomoLiteTrayInspectNative]::HitOwner($centerX, $centerY)
    if (-not [MihomoLiteTrayInspectNative]::MoveCursor($centerX, $centerY)) { throw 'Could not hover the icon rectangle.' }
    $tooltip = Read-TooltipEvidence $centerX $centerY $shellProcessId
    $result.TooltipVerified = $tooltip.Verified
    $result.ObservedTooltip = $tooltip.Observed
    $result.TooltipSource = $tooltip.Source
    if (-not $tooltip.Verified) { throw 'Neither bounded shell-owned WM_GETTEXT nor UIAutomation could confirm ExpectedTooltip; screenshot evidence remains available.' }
} catch {
    $result.Reason = $_.Exception.Message
} finally {
    if ($nativeReady) {
        if ($null -ne $originalCursor) {
            try { $result.CursorRestored = [MihomoLiteTrayInspectNative]::MoveCursor($originalCursor.X, $originalCursor.Y) }
            catch { $result.Diagnostics += ('Cursor restoration: ' + $_.Exception.Message) }
        }
        if ($null -ne $result.OriginalOverflowOpen) {
            try {
                $currentOpen = [MihomoLiteTrayInspectNative]::OverflowOpen()
                if ($currentOpen -ne $result.OriginalOverflowOpen -and $null -ne $overflowButton) {
                    # Invoke only the system overflow button, never WM_CLOSE,
                    # application commands, Escape, or clicks on user windows.
                    Invoke-OverflowButton $overflowButton
                    $watch = [Diagnostics.Stopwatch]::StartNew()
                    do { Start-Sleep -Milliseconds 100; $currentOpen = [MihomoLiteTrayInspectNative]::OverflowOpen() }
                    while ($currentOpen -ne $result.OriginalOverflowOpen -and $watch.ElapsedMilliseconds -lt 2000)
                }
                $result.OverflowRestored = ([MihomoLiteTrayInspectNative]::OverflowOpen() -eq $result.OriginalOverflowOpen)
                if ($overflowTouched -and -not $result.OverflowRestored) { $result.Diagnostics += 'Changed overflow state could not be restored reliably.' }
            } catch { $result.Diagnostics += ('Overflow restoration: ' + $_.Exception.Message) }
        }
        try { $result.DpiContextRestored = [MihomoLiteTrayInspectNative]::RestoreDpiContext($oldDpiContext) }
        catch { $result.Diagnostics += ('Thread DPI context restoration: ' + $_.Exception.Message) }
    }
}
$result.Supported = $result.ScreenshotCaptured -and $result.TooltipVerified -and $result.CursorRestored -and $result.OverflowRestored -and $result.DpiContextRestored
if (-not $result.Supported -and [string]::IsNullOrEmpty($result.Reason)) { $result.Reason = 'Capture/tooltip verification or restoration was unsupported; inspect component fields and diagnostics.' }
$result.FinishedAtUtc = [DateTime]::UtcNow.ToString('o')
$json = $result | ConvertTo-Json -Depth 12
if ($writeSidecar) {
    try { [IO.File]::WriteAllText($sidecarPath, $json, [Text.UTF8Encoding]::new($false)) }
    catch { $result.Supported = $false; $result.Diagnostics += ('JSON output failed: ' + $_.Exception.Message); $json = $result | ConvertTo-Json -Depth 12 }
}
Write-Output $json
