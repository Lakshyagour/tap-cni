package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ipam"
	"github.com/containernetworking/plugins/pkg/ns"
	logging "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"time"
)

func init() {
	file, err := os.OpenFile("/var/log/cni-tap-plugin.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		logging.Fatal(err)
	}
	logging.SetOutput(file)
	logging.SetLevel(logging.DebugLevel)
}

type NetConf struct {
	types.NetConf
	IPAM struct {
		Type   string `json:"type"`
		Subnet string `json:"subnet"`
		Routes []struct {
			Dst string `json:"dst"`
		} `json:"routes"`
	} `json:"ipam"`
	DNS types.DNS `json:"dns"`
}

func main() {
	skel.PluginMain(cmdAdd, cmdCheck, cmdDel, version.All, "CNI TAP plugin")
}

func loadNetConf(bytes []byte) (*NetConf, error) {
	n := &NetConf{}
	if err := json.Unmarshal(bytes, n); err != nil {
		return nil, fmt.Errorf("failed to load netconf: %v", err)
	}
	return n, nil
}

func cmdAdd(args *skel.CmdArgs) error {
	var result *current.Result

	conf, err := loadNetConf(args.StdinData)
	if err != nil {
		return err
	}

	tapName := "tap" + args.ContainerID[:5]
	cmd := exec.Command("tapudsserver", tapName)

	if err := cmd.Start(); err != nil {
		logging.Infof("Error starting command: %q", err)
	}

	errr := cmd.Process.Release()
	if errr != nil {
		logging.Infof("Error releasing process resources: %q", err)
	}

	logging.Infof("Command executed successfully %q", conf.Name)

	time.Sleep(1 * time.Second)

	logging.Infof("cmdAdd(): getting tap device from name")
	device, err := netlink.LinkByName(tapName)
	if err != nil {
		err = fmt.Errorf("cmdAdd(): failed to find device: %w", err)
		logging.Errorf(err.Error())
		return err
	}

	containerNs, err := ns.GetNS(args.Netns)
	if err != nil {
		err = fmt.Errorf("cmdAdd(): failed to open container netns %q: %w", args.Netns, err)
		logging.Errorf(err.Error())
		return err
	}
	defer containerNs.Close()

	logging.Infof("cmdAdd(): moving tap device from default to container network namespace")
	if err := netlink.LinkSetNsFd(device, int(containerNs.Fd())); err != nil {
		err = fmt.Errorf("cmdAdd(): failed to move device %q to container netns: %w", device.Attrs().Name, err)
		logging.Errorf(err.Error())
		return err
	}

	if err := containerNs.Do(func(_ ns.NetNS) error {
		logging.Infof("cmdAdd(): set device to UP state")
		if err := netlink.LinkSetUp(device); err != nil {
			err = fmt.Errorf("cmdAdd(): failed to set device %q to UP state: %w", device.Attrs().Name, err)
			logging.Errorf(err.Error())
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	if conf.IPAM.Type != "" {
		result, err = getIPAM(args, conf, device, containerNs)
		if err != nil {
			err = fmt.Errorf("cmdAdd(): error configuring IPAM on device %q: %w", device.Attrs().Name, err)
			logging.Errorf(err.Error())
			return err
		}
		result, err = setIPAM(conf, result, device, containerNs)
		if err != nil {
			err = fmt.Errorf("cmdAdd(): error configuring IPAM on device netns %q: %w", device.Attrs().Name, err)
			logging.Errorf(err.Error())

			return err
		}
		return types.PrintResult(result, conf.CNIVersion)
	}

	return printLink(device, conf.CNIVersion, containerNs)
}



func cmdDel(args *skel.CmdArgs) error {

	conf, err := loadNetConf(args.StdinData)
	if err != nil {
		return err
	}
	tapName := "tap" + args.ContainerID[:5]

	logging.Infof("cmdDel(): getting container network namespace")
	containerNs, err := ns.GetNS(args.Netns)
	if err != nil {
		err = fmt.Errorf("cmdDel(): failed to open container netns %q: %w", args.Netns, err)
		logging.Errorf(err.Error())

		return err
	}
	defer containerNs.Close()

	logging.Infof("cmdDel(): getting default network namespace")
	defaultNs, err := ns.GetCurrentNS()
	if err != nil {
		err = fmt.Errorf("cmdDel(): failed to open default netns %q: %w", args.Netns, err)
		logging.Errorf(err.Error())

		return err
	}
	defer defaultNs.Close()

	logging.Infof("cmdDel(): executing within container network namespace:")
	if err := containerNs.Do(func(_ ns.NetNS) error {

		logging.Infof("cmdDel(): getting device from name")
		device, err := netlink.LinkByName(tapName)
		if err != nil {
			err = fmt.Errorf("cmdDel(): failed to find device %q in containerNS: %w", tapName, err)
			logging.Errorf(err.Error())

			return err
		}

		logging.Infof("cmdDel(): moving device from container to default network namespace")
		if err = netlink.LinkSetNsFd(device, int(defaultNs.Fd())); err != nil {
			err = fmt.Errorf("cmdDel(): failed to move %q to host netns: %w", device.Attrs().Alias, err)
			logging.Errorf(err.Error())

			return err
		}

		return nil
	}); err != nil {
		return err
	}

	cmd := exec.Command("ip", "link", "del", tapName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete tap device: %v", err)
	}

	logging.Infof("cmdDel(): cleaning IPAM config on device")
	if conf.IPAM.Type != "" {
		if err := ipam.ExecDel(conf.IPAM.Type, args.StdinData); err != nil {
			return err
		}
	}

	return nil
}

func getIPAM(args *skel.CmdArgs, cfg *NetConf, device netlink.Link, netns ns.NetNS) (*current.Result, error) {
	logging.Infof("getIPAM(): running IPAM plugin: %s", cfg.IPAM.Type)
	ipamResult, err := ipam.ExecAdd(cfg.IPAM.Type, args.StdinData)
	if err != nil {
		err = fmt.Errorf("getIPAM(): failed to get IPAM result: %v", err)
		logging.Errorf(err.Error())
		return nil, err
	}

	logging.Debugf("getIPAM(): IPAM result: %+v", ipamResult)

	defer func() {
		if err != nil {
			logging.Debugf("getIPAM(): An error occurred. Deleting IPAM to prevent IP leak.")
			err := ipam.ExecDel(cfg.IPAM.Type, args.StdinData)
			if err != nil {
				logging.Errorf("getIPAM(): Error while deleting IPAM: %v", err)
			}
		}
	}()

	result, err := current.NewResultFromResult(ipamResult)
	if err != nil {
		err = fmt.Errorf("getIPAM(): failed to convert IPAM result: %v", err)
		logging.Errorf(err.Error())
		return nil, err
	}

	result.Interfaces = []*current.Interface{{
		Name:    device.Attrs().Name,
		Mac:     device.Attrs().HardwareAddr.String(),
		Sandbox: netns.Path(),
	}}
	for _, ipc := range result.IPs {
		ipc.Interface = current.Int(0)
	}

	return result, nil
}

func printLink(dev netlink.Link, cniVersion string, containerNs ns.NetNS) error {
	result := current.Result{
		CNIVersion: current.ImplementedSpecVersion,
		Interfaces: []*current.Interface{{
			Name:    dev.Attrs().Name,
			Mac:     dev.Attrs().HardwareAddr.String(),
			Sandbox: containerNs.Path(),
		}},
	}
	return types.PrintResult(&result, cniVersion)
}

func setIPAM(cfg *NetConf, result *current.Result, device netlink.Link, netns ns.NetNS) (*current.Result, error) {
	logging.Infof("configureIPAM(): executing within container netns")
	if err := netns.Do(func(_ ns.NetNS) error {

		logging.Infof("configureIPAM(): setting device IP")
		if err := ipam.ConfigureIface(device.Attrs().Name, result); err != nil {
			err = fmt.Errorf("configureIPAM(): Error setting IPAM on device %q: %w", device.Attrs().Name, err)
			logging.Errorf(err.Error())

			return err
		}

		return nil
	}); err != nil {
		return result, err
	}

	result.DNS = cfg.DNS

	return result, nil
}

func cmdCheck(args *skel.CmdArgs) error {
	return nil
}