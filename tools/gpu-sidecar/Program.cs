// xollama-gpu-sidecar — reads the GPU readings D3DKMT does not expose (temp,
// power, clocks, fan) from LibreHardwareMonitor and writes them to the metrics
// file /api/xollama/system merges in (see docs/features/gpu-metrics-sidecar.md).
//
// Usage:
//   xollama-gpu-sidecar [--out PATH] [--interval SECONDS] [--once] [--no-cpu] [--parent PID]
//
// --out       where to write the JSON (default %LOCALAPPDATA%\xOllama\gpu-metrics.json)
// --interval  seconds between writes (default 2)
// --once      write one snapshot and exit (smoke test)
// --no-cpu    do not open the CPU (skips the Ryzen SMU; an APU's iGPU GFX
//             readings may then be missing)
// --parent    exit when this process does (the xollama server that started it)

using System.Diagnostics;
using System.Globalization;
using System.Text.Json;
using System.Text.Json.Serialization;
using LibreHardwareMonitor.Hardware;
using Xollama.GpuSidecar;

var outPath = Path.Combine(
    Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
    "xOllama", "gpu-metrics.json");
int interval = 2;
bool once = false, cpu = true, dump = false;
Process? parent = null;

for (int i = 0; i < args.Length; i++)
{
    switch (args[i])
    {
        case "--out": outPath = args[++i]; break;
        case "--interval": interval = int.Parse(args[++i], CultureInfo.InvariantCulture); break;
        case "--once": once = true; break;
        case "--no-cpu": cpu = false; break;
        case "--dump": dump = true; break;
        case "--parent":
            try
            {
                parent = Process.GetProcessById(int.Parse(args[++i], CultureInfo.InvariantCulture));
            }
            catch (ArgumentException)
            {
                return 0; // already gone: nobody to write for
            }
            break;
        case "-h" or "--help":
            Console.WriteLine("xollama-gpu-sidecar [--out PATH] [--interval SECONDS] [--once] [--no-cpu] [--parent PID] [--dump]");
            return 0;
        default:
            Console.Error.WriteLine($"unknown argument: {args[i]}");
            return 2;
    }
}

var computer = new Computer
{
    IsGpuEnabled = true,
    IsCpuEnabled = cpu, // the APU's iGPU GFX readings come from the Ryzen SMU
};

try
{
    computer.Open();
}
catch (Exception ex)
{
    Console.Error.WriteLine($"cannot open hardware monitor (admin may be required for SMU): {ex.Message}");
    // GPU-only sensors usually still work without the kernel driver; keep going.
}

var visitor = new UpdateVisitor();

if (dump)
{
    computer.Accept(visitor);
    foreach (var hw in computer.Hardware)
    {
        if (hw.HardwareType is not (HardwareType.GpuAmd or HardwareType.GpuIntel or HardwareType.GpuNvidia))
            continue;
        Console.WriteLine($"[{hw.HardwareType}] {hw.Name}");
        foreach (var s in hw.Sensors)
        {
            string val = s.Value is float v && !float.IsNaN(v) ? v.ToString("0.###", CultureInfo.InvariantCulture) : "null";
            string max = s.Max is float m && !float.IsNaN(m) ? m.ToString("0.###", CultureInfo.InvariantCulture) : "null";
            Console.WriteLine($"    {s.SensorType,-12} | {s.Name,-28} | value={val,-10} max={max}");
        }
    }
    computer.Close();
    return 0;
}

using var cancel = new CancellationTokenSource();
Console.CancelKeyPress += (_, e) => { e.Cancel = true; cancel.Cancel(); };

Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(outPath))!);

do
{
    computer.Accept(visitor);
    var gpus = CollectGpus(computer);
    var doc = new SidecarFile
    {
        Version = 1,
        UpdatedUnix = DateTimeOffset.UtcNow.ToUnixTimeSeconds(),
        Gpus = gpus,
    };
    WriteAtomic(outPath, doc);

    if (once)
    {
        Console.WriteLine($"wrote {gpus.Count} GPU(s) to {outPath}:");
        Console.WriteLine(JsonSerializer.Serialize(doc, PrettyJson.Default.SidecarFile));
        break;
    }
}
while (!cancel.Token.WaitHandle.WaitOne(TimeSpan.FromSeconds(interval)) && !ParentGone(parent));

computer.Close();
return 0;

static List<SidecarGpu> CollectGpus(IComputer computer)
{
    // PCI location and negotiated PCIe link, read in user context (no admin)
    // from the device-property store; consumed by name as we meet each GPU.
    var pci = SetupApi.DisplayAdapters();
    var usedPci = new HashSet<int>();

    var gpus = new List<SidecarGpu>();
    foreach (var hw in computer.Hardware)
    {
        bool amd = hw.HardwareType == HardwareType.GpuAmd;
        bool intel = hw.HardwareType == HardwareType.GpuIntel;
        bool nvidia = hw.HardwareType == HardwareType.GpuNvidia;
        if (!amd && !intel && !nvidia)
            continue;

        var g = new SidecarGpu
        {
            Name = hw.Name,
            TempC = SensorValue(hw, SensorType.Temperature, "Core") ?? SensorValue(hw, SensorType.Temperature, null),
            PowerW = SensorValue(hw, SensorType.Power, "Package") ?? SensorValue(hw, SensorType.Power, "Total")
                     ?? SensorValue(hw, SensorType.Power, null),
            ClockMhz = ToUint(SensorValue(hw, SensorType.Clock, "Core") ?? SensorValue(hw, SensorType.Clock, null)),
            ClockMaxMhz = ToUint(SensorMax(hw, SensorType.Clock, "Core") ?? SensorMax(hw, SensorType.Clock, null)),
            FanPct = SensorValue(hw, SensorType.Control, "Fan") ?? SensorValue(hw, SensorType.Control, null),
            UtilPct = SensorValue(hw, SensorType.Load, "Core") ?? SensorValue(hw, SensorType.Load, null),
        };

        // An AMD part named "...Graphics" with no discrete model marker is an
        // APU's integrated GPU; the server matches it by the Integrated flag.
        if (amd && IsIntegratedName(hw.Name))
            g.Integrated = true;

        // Attach the PCI id and negotiated link from the matching display
        // adapter (by name); gives the server a precise match key and the link
        // LibreHardwareMonitor cannot read.
        for (int i = 0; i < pci.Count; i++)
        {
            if (usedPci.Contains(i) || !string.Equals(pci[i].Name, hw.Name, StringComparison.OrdinalIgnoreCase))
                continue;
            usedPci.Add(i);
            var p = pci[i];
            g.PciId = p.PciId;
            if (p.Gen != null || p.Width != null || p.MaxGen != null || p.MaxWidth != null)
                g.Link = new SidecarLink { Gen = p.Gen, Width = p.Width, MaxGen = p.MaxGen, MaxWidth = p.MaxWidth };
            break;
        }

        gpus.Add(g);
    }
    return gpus;
}

static bool IsIntegratedName(string name)
{
    string n = name.ToLowerInvariant();
    if (n.Contains(" rx ") || n.Contains("radeon pro") || n.Contains("instinct") || n.Contains("w6") || n.Contains("w7"))
        return false;
    return n.Contains("graphics") || n.Contains("vega") || n.Contains("radeon(tm) graphics");
}

static float? SensorValue(IHardware hw, SensorType type, string? nameContains)
{
    foreach (var s in hw.Sensors)
        if (s.SensorType == type && s.Value is float v && !float.IsNaN(v)
            && (nameContains == null || s.Name.Contains(nameContains, StringComparison.OrdinalIgnoreCase)))
            return v;
    return null;
}

static float? SensorMax(IHardware hw, SensorType type, string? nameContains)
{
    foreach (var s in hw.Sensors)
        if (s.SensorType == type && s.Max is float v && !float.IsNaN(v)
            && (nameContains == null || s.Name.Contains(nameContains, StringComparison.OrdinalIgnoreCase)))
            return v;
    return null;
}

static ulong? ToUint(float? v) => v is float f && f > 0 ? (ulong)Math.Round(f) : null;

// The server binds the sidecar to itself with a kill-on-close job; --parent is
// the second guard, for a server that could not make one.
static bool ParentGone(Process? parent)
{
    if (parent == null)
        return false;
    try
    {
        return parent.HasExited;
    }
    catch (InvalidOperationException)
    {
        return true;
    }
}

// The rename fails while the server has the file open for its read (Go opens
// without FILE_SHARE_DELETE), so it is tried a few times and otherwise skipped:
// the next interval writes again, and the server reads a file up to 10 s old.
static void WriteAtomic(string path, SidecarFile doc)
{
    string tmp = path + ".tmp";
    try
    {
        File.WriteAllText(tmp, JsonSerializer.Serialize(doc, CompactJson.Default.SidecarFile));
    }
    catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
    {
        Console.Error.WriteLine($"cannot write {tmp}: {ex.Message}");
        return;
    }
    for (int attempt = 0; ; attempt++)
    {
        try
        {
            File.Move(tmp, path, overwrite: true);
            return;
        }
        catch (Exception ex) when ((ex is IOException or UnauthorizedAccessException) && attempt < 5)
        {
            Thread.Sleep(50);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            Console.Error.WriteLine($"cannot replace {path}: {ex.Message}");
            return;
        }
    }
}

sealed class UpdateVisitor : IVisitor
{
    public void VisitComputer(IComputer computer) => computer.Traverse(this);
    public void VisitHardware(IHardware hardware)
    {
        hardware.Update();
        foreach (var sub in hardware.SubHardware) sub.Accept(this);
    }
    public void VisitSensor(ISensor sensor) { }
    public void VisitParameter(IParameter parameter) { }
}

sealed class SidecarFile
{
    [JsonPropertyName("version")] public int Version { get; set; }
    [JsonPropertyName("updated_unix")] public long UpdatedUnix { get; set; }
    [JsonPropertyName("gpus")] public List<SidecarGpu> Gpus { get; set; } = new();
}

sealed class SidecarGpu
{
    [JsonPropertyName("pci_id")] public string? PciId { get; set; }
    [JsonPropertyName("name")] public string? Name { get; set; }
    [JsonPropertyName("integrated")] public bool Integrated { get; set; }
    [JsonPropertyName("temp_c")] public float? TempC { get; set; }
    [JsonPropertyName("power_w")] public float? PowerW { get; set; }
    [JsonPropertyName("power_limit_w")] public float? PowerLimitW { get; set; }
    [JsonPropertyName("clock_mhz")] public ulong? ClockMhz { get; set; }
    [JsonPropertyName("clock_max_mhz")] public ulong? ClockMaxMhz { get; set; }
    [JsonPropertyName("fan_pct")] public float? FanPct { get; set; }
    [JsonPropertyName("util_pct")] public float? UtilPct { get; set; }
    [JsonPropertyName("link")] public SidecarLink? Link { get; set; }
}

sealed class SidecarLink
{
    [JsonPropertyName("gen")] public int? Gen { get; set; }
    [JsonPropertyName("width")] public int? Width { get; set; }
    [JsonPropertyName("max_gen")] public int? MaxGen { get; set; }
    [JsonPropertyName("max_width")] public int? MaxWidth { get; set; }
}

// Source-generated, not reflection: the shipped exe is trimmed, and a trimmed
// .NET 8 app has reflection-based System.Text.Json switched off.
[JsonSourceGenerationOptions(DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull)]
[JsonSerializable(typeof(SidecarFile))]
partial class CompactJson : JsonSerializerContext { }

[JsonSourceGenerationOptions(WriteIndented = true, DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull)]
[JsonSerializable(typeof(SidecarFile))]
partial class PrettyJson : JsonSerializerContext { }
