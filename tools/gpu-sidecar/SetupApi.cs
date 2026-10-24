// Reads, in user context (no admin, no kernel driver), the PCI location and the
// negotiated PCIe link of each display adapter. The PCI bus driver publishes the
// PCIe Link Status register as device properties (DEVPKEY_PciDevice_*); SetupAPI
// reads them. This is what Device Manager shows, and it is the *negotiated* link
// (e.g. a card in an x16 slot routed x4 through the chipset reports x4), not a
// card maximum.

using System.Runtime.InteropServices;

namespace Xollama.GpuSidecar;

internal sealed record PciDisplay(
    string Name, string? PciId,
    int? Gen, int? Width, int? MaxGen, int? MaxWidth);

internal static class SetupApi
{
    public static List<PciDisplay> DisplayAdapters()
    {
        var list = new List<PciDisplay>();
        var disp = GUID_DEVCLASS_DISPLAY;
        nint h = SetupDiGetClassDevs(ref disp, null, 0, DIGCF_PRESENT);
        if (h == INVALID_HANDLE_VALUE)
            return list;
        try
        {
            var dev = new SP_DEVINFO_DATA { cbSize = (uint)Marshal.SizeOf<SP_DEVINFO_DATA>() };
            for (uint i = 0; SetupDiEnumDeviceInfo(h, i, ref dev); i++)
            {
                string name = RegString(h, ref dev, SPDRP_DEVICEDESC) ?? "";
                string? pci = PciAddress(h, ref dev);
                // Both the negotiated link (what the device runs at now — a card
                // in an x16 slot routed x4 through the chipset runs x4) and the
                // card maximum.
                list.Add(new PciDisplay(
                    name, pci,
                    SpeedToGen(PropU32(h, ref dev, DEVPKEY_PciDevice_CurrentLinkSpeed)),
                    (int?)PropU32(h, ref dev, DEVPKEY_PciDevice_CurrentLinkWidth),
                    SpeedToGen(PropU32(h, ref dev, DEVPKEY_PciDevice_MaxLinkSpeed)),
                    (int?)PropU32(h, ref dev, DEVPKEY_PciDevice_MaxLinkWidth)));
            }
        }
        finally
        {
            SetupDiDestroyDeviceInfoList(h);
        }
        return list;
    }

    // The PCIe link-speed property is an enum: 1=2.5 (Gen1) .. 5=32 GT/s (Gen5).
    private static int? SpeedToGen(uint? code) => code is uint c && c is >= 1 and <= 6 ? (int)c : null;

    private static string? PciAddress(nint h, ref SP_DEVINFO_DATA dev)
    {
        if (RegU32(h, ref dev, SPDRP_BUSNUMBER) is not uint bus)
            return null;
        if (RegU32(h, ref dev, SPDRP_ADDRESS) is not uint addr)
            return null;
        uint device = addr >> 16, func = addr & 0xffff;
        return $"0000:{bus:x2}:{device:x2}.{func:x}";
    }

    private static uint? PropU32(nint h, ref SP_DEVINFO_DATA dev, DEVPROPKEY key)
    {
        var buf = new byte[4];
        if (SetupDiGetDevicePropertyW(h, ref dev, ref key, out uint type, buf, (uint)buf.Length, out uint need, 0)
            && type == DEVPROP_TYPE_UINT32 && need == 4)
            return BitConverter.ToUInt32(buf, 0);
        return null;
    }

    private static uint? RegU32(nint h, ref SP_DEVINFO_DATA dev, uint prop)
    {
        var buf = new byte[4];
        if (SetupDiGetDeviceRegistryPropertyW(h, ref dev, prop, out _, buf, (uint)buf.Length, out uint need)
            && need == 4)
            return BitConverter.ToUInt32(buf, 0);
        return null;
    }

    private static string? RegString(nint h, ref SP_DEVINFO_DATA dev, uint prop)
    {
        var buf = new byte[512];
        if (SetupDiGetDeviceRegistryPropertyW(h, ref dev, prop, out _, buf, (uint)buf.Length, out uint need) && need >= 2)
            return System.Text.Encoding.Unicode.GetString(buf, 0, (int)need).TrimEnd('\0');
        return null;
    }

    // --- interop ---

    private static readonly nint INVALID_HANDLE_VALUE = -1;
    private const uint DIGCF_PRESENT = 0x02;
    private const uint SPDRP_DEVICEDESC = 0x00;
    private const uint SPDRP_ADDRESS = 0x1c;
    private const uint SPDRP_BUSNUMBER = 0x15;
    private const uint DEVPROP_TYPE_UINT32 = 0x07;
    private static Guid GUID_DEVCLASS_DISPLAY = new("4d36e968-e325-11ce-bfc1-08002be10318");

    // pciprop.h: {3AB22E31-8264-4b4e-9AF5-A8D2D8E33E62}
    private static readonly Guid PciDeviceGuid = new("3AB22E31-8264-4b4e-9AF5-A8D2D8E33E62");
    private static readonly DEVPROPKEY DEVPKEY_PciDevice_MaxLinkSpeed = new(PciDeviceGuid, 11);
    private static readonly DEVPROPKEY DEVPKEY_PciDevice_MaxLinkWidth = new(PciDeviceGuid, 12);
    private static readonly DEVPROPKEY DEVPKEY_PciDevice_CurrentLinkSpeed = new(PciDeviceGuid, 9);
    private static readonly DEVPROPKEY DEVPKEY_PciDevice_CurrentLinkWidth = new(PciDeviceGuid, 10);

    [StructLayout(LayoutKind.Sequential)]
    private struct SP_DEVINFO_DATA
    {
        public uint cbSize;
        public Guid ClassGuid;
        public uint DevInst;
        public nint Reserved;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct DEVPROPKEY
    {
        public Guid fmtid;
        public uint pid;
        public DEVPROPKEY(Guid g, uint p) { fmtid = g; pid = p; }
    }

    [DllImport("setupapi.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern nint SetupDiGetClassDevs(ref Guid ClassGuid, string? Enumerator, nint hwndParent, uint Flags);

    [DllImport("setupapi.dll", SetLastError = true)]
    private static extern bool SetupDiEnumDeviceInfo(nint DeviceInfoSet, uint MemberIndex, ref SP_DEVINFO_DATA DeviceInfoData);

    [DllImport("setupapi.dll", SetLastError = true)]
    private static extern bool SetupDiDestroyDeviceInfoList(nint DeviceInfoSet);

    [DllImport("setupapi.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool SetupDiGetDevicePropertyW(
        nint DeviceInfoSet, ref SP_DEVINFO_DATA DeviceInfoData, ref DEVPROPKEY PropertyKey,
        out uint PropertyType, byte[] PropertyBuffer, uint PropertyBufferSize, out uint RequiredSize, uint Flags);

    [DllImport("setupapi.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool SetupDiGetDeviceRegistryPropertyW(
        nint DeviceInfoSet, ref SP_DEVINFO_DATA DeviceInfoData, uint Property,
        out uint PropertyRegDataType, byte[] PropertyBuffer, uint PropertyBufferSize, out uint RequiredSize);
}
