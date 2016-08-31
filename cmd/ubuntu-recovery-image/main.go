package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"gopkg.in/yaml.v2"
)

import rplib "github.com/Lyoncore/ubuntu-recovery-rplib"
import utils "github.com/Lyoncore/ubuntu-recovery-image/utils"

var version string
var commit string
var commitstamp string

// setupLoopDevice setup loop device for base image and recovery image.
func setupLoopDevice(recoveryOutputFile string, recoveryNR string) (string, string) {
	log.Printf("[SETUP_LOOPDEVICE]")
	basefile, err := os.Open(configs.Yaml.Configs.BaseImage)
	rplib.Checkerr(err)
	defer basefile.Close()
	basefilest, err := basefile.Stat()
	rplib.Checkerr(err)

	log.Printf("fallocate %d bytes for %q\n", basefilest.Size(), configs.Yaml.Configs.BaseImage)
	outputfile, err := os.Create(recoveryOutputFile)
	rplib.Checkerr(err)
	defer outputfile.Close()

	syscall.Fallocate(int(outputfile.Fd()), 0, 0, basefilest.Size())
	log.Printf("[setup a loopback device for recovery image %s]", recoveryOutputFile)
	recoveryImageLoop := rplib.Shellcmdoutput(fmt.Sprintf("losetup --find --show %s | xargs basename", recoveryOutputFile))

	log.Printf("[setup a readonly loopback device for base image]")
	baseImageLoop := rplib.Shellcmdoutput(fmt.Sprintf("losetup -r --find --show %s | xargs basename", configs.Yaml.Configs.BaseImage))

	log.Printf("[create %s partition on %s]", recoveryOutputFile, recoveryImageLoop)

	recoveryBegin := 4
	recoverySize, err := strconv.Atoi(configs.Yaml.Configs.RecoverySize)
	rplib.Checkerr(err)
	recoveryEnd := recoveryBegin + recoverySize

	rplib.Shellexec("parted", "-ms", "-a", "optimal", fmt.Sprintf("/dev/%s", recoveryImageLoop),
		"unit", "MiB",
		"mklabel", "msdos",
		"mkpart", "primary", "fat32", fmt.Sprintf("%d", recoveryBegin), fmt.Sprintf("%d", recoveryEnd),
		"set", recoveryNR, "boot", "on",
		"print")

	return baseImageLoop, recoveryImageLoop
}

func findSnap(folder, input string) string {
	name := rplib.FindSnapName(input)

	if "" == name {
		name = input
	}
	log.Printf("findSnap: %s/%s_*.snap", folder, name)
	paths, err := filepath.Glob(fmt.Sprintf("%s/%s_*.snap", folder, name))
	rplib.Checkerr(err)
	if 1 != len(paths) {
		log.Println("paths:", paths)
		log.Panic("Should have one and only one specified snap")
	}
	path := paths[0]
	log.Printf("snap path:", path)
	return path
}

func setupInitrd(initrdImagePath string, tmpDir string) {
	log.Printf("[SETUP_INITRD]")

	initrdTmpDir := fmt.Sprintf("%s/misc/initrd/", tmpDir)
	log.Printf("[setup %s/misc/initrd]", tmpDir)
	err := os.MkdirAll(initrdTmpDir, 0755)
	rplib.Checkerr(err)
	defer os.RemoveAll(initrdTmpDir)

	log.Printf("[processiing kernel snaps]")
	kernelsnapTmpDir := fmt.Sprintf("%s/misc/kernel-snap", tmpDir)
	err = os.MkdirAll(kernelsnapTmpDir, 0755)
	rplib.Checkerr(err)
	defer os.RemoveAll(kernelsnapTmpDir)

	log.Printf("[locate kernel snap and mount]")
	kernelSnapPath := findSnap(fmt.Sprintf("%s/image/writable/system-data/var/lib/snapd/snaps/", tmpDir), configs.Yaml.Snaps.Kernel)

	rplib.Shellexec("mount", kernelSnapPath, kernelsnapTmpDir)
	defer syscall.Unmount(kernelsnapTmpDir, 0)

	log.Printf("[copy kernel.img]")
	rplib.Shellexec("cp", "-f", fmt.Sprintf("%s/kernel.img", kernelsnapTmpDir), fmt.Sprintf("%s/device/%s/", tmpDir, configs.Yaml.Recovery.FsLabel))

	log.Printf("[unxz initrd in kernel snap]")
	unxzInitrdCmd := fmt.Sprintf("unxz < %s/initrd.img | (cd %s; cpio -i )", kernelsnapTmpDir, initrdTmpDir)
	_ = rplib.Shellcmdoutput(unxzInitrdCmd)

	log.Printf("[modify initrd ORDER file]")
	orderFile := fmt.Sprintf("%s/scripts/local-premount/ORDER", initrdTmpDir)
	orderData, err := ioutil.ReadFile(orderFile)
	rplib.Checkerr(err)

	orderDataInsertFront := []byte("[ -e /conf/param.conf ] && . /conf/param.conf\n/scripts/local-premount/00_recovery $@\n")
	err = ioutil.WriteFile(orderFile, append(orderDataInsertFront[:], orderData[:]...), 0755)
	rplib.Checkerr(err)

	log.Printf("[create initrd recovery script]")
	recoveryInitrdScript, err := ioutil.ReadFile("data/00_recovery")
	rplib.Checkerr(err)
	err = ioutil.WriteFile(fmt.Sprintf("%s/scripts/local-premount/00_recovery", initrdTmpDir), recoveryInitrdScript, 0755)
	rplib.Checkerr(err)

	log.Printf("[recreate initrd]")
	_ = rplib.Shellcmdoutput(fmt.Sprintf("( cd %s; find | cpio --quiet -o -H newc ) | xz -c9 --check=crc32 > %s", initrdTmpDir, initrdImagePath))
}

func createBaseImage() {
	fmt.Printf("Create base image, channel: %s\n", configs.Yaml.Configs.Channel)

	if _, err := os.Stat(configs.Yaml.Configs.BaseImage); err == nil {
		fmt.Printf("The file %s exist, remove the file.\n", configs.Yaml.Configs.BaseImage)
		os.Remove(configs.Yaml.Configs.BaseImage)
	}

	rplib.Shellexec(configs.Yaml.Udf.Binary, configs.Yaml.Udf.Option, configs.Yaml.Configs.Release,
		configs.Opt.Store, configs.Yaml.Configs.Store,
		configs.Opt.Device, configs.Yaml.Configs.Device,
		configs.Opt.Channel, configs.Yaml.Configs.Channel,
		configs.Opt.BaseImage, configs.Yaml.Configs.BaseImage,
		configs.Opt.Ssh,
		configs.Opt.Size, configs.Yaml.Configs.Size,
		configs.Opt.Devmode,
		configs.Opt.Kernel, configs.Yaml.Snaps.Kernel,
		configs.Opt.Os, configs.Yaml.Snaps.Os,
		configs.Opt.Gadget, configs.Yaml.Snaps.Gadget)
}

func createRecoveryImage(recoveryNR string, recoveryOutputFile string, buildstamp utils.BuildStamp) {
	// Setup loop devices
	baseImageLoop, recoveryImageLoop := setupLoopDevice(recoveryOutputFile, recoveryNR)
	// Delete loop devices
	defer rplib.Shellcmd(fmt.Sprintf("losetup -d /dev/%s", baseImageLoop))
	defer rplib.Shellcmd(fmt.Sprintf("losetup -d /dev/%s", recoveryImageLoop))
	log.Printf("[base image loop:%s, recovery image loop: %s created]\n", baseImageLoop, recoveryImageLoop)

	// Create device maps from partition tables
	log.Printf("[kpartx]")
	rplib.Shellexec("kpartx", "-a", fmt.Sprintf("/dev/%s", baseImageLoop))
	rplib.Shellexec("kpartx", "-a", fmt.Sprintf("/dev/%s", recoveryImageLoop))
	rplib.Shellexec("udevadm", "settle")
	// Delete device maps
	defer rplib.Shellexec("udevadm", "settle")
	defer rplib.Shellexec("kpartx", "-d", fmt.Sprintf("/dev/%s", recoveryImageLoop))
	defer rplib.Shellexec("kpartx", "-d", fmt.Sprintf("/dev/%s", baseImageLoop))

	// TODO: rewritten with launchpad/goget-ubuntu-touch/DiskImage image.Create
	log.Printf("[mkfs.fat]")
	rplib.Shellexec("mkfs.fat", "-F", "32", "-n", configs.Yaml.Recovery.FsLabel, fmt.Sprintf("/dev/mapper/%sp%s", recoveryImageLoop, recoveryNR))

	tmpDir, err := ioutil.TempDir("", "")
	rplib.Checkerr(err)

	log.Printf("tmpDir:", tmpDir)
	defer os.RemoveAll(tmpDir) // clean up

	recoveryMapperDevice := fmt.Sprintf("/dev/mapper/%sp%s", recoveryImageLoop, recoveryNR)
	recoveryDir := fmt.Sprintf("%s/device/%s/", tmpDir, configs.Yaml.Recovery.FsLabel)
	log.Printf("[mkdir %s]", recoveryDir)

	err = os.MkdirAll(recoveryDir, 0755)
	rplib.Checkerr(err)

	log.Printf("[mount device %s on recovery dir %s]", recoveryMapperDevice, recoveryDir)
	err = syscall.Mount(recoveryMapperDevice, recoveryDir, "vfat", 0, "")
	rplib.Checkerr(err)
	defer syscall.Unmount(recoveryDir, 0)

	baseMapperDeviceGlobName := fmt.Sprintf("/dev/mapper/%s*", baseImageLoop)
	baseMapperDeviceArray, err := filepath.Glob(baseMapperDeviceGlobName)
	rplib.Checkerr(err)

	for _, part := range baseMapperDeviceArray {
		label := rplib.Shellexecoutput("blkid", part, "-o", "value", "-s", "LABEL")
		if match, _ := regexp.MatchString("system-boot|writable", label); match {
			log.Printf("matched")
			baseDir := fmt.Sprintf("%s/image/%s", tmpDir, label)
			err := os.MkdirAll(baseDir, 0755)
			rplib.Checkerr(err)
			defer os.RemoveAll(baseDir) // clean up

			log.Printf("[mount device %s on base image dir %s]", part, label)
			fstype := rplib.Shellexecoutput("blkid", part, "-o", "value", "-s", "TYPE")
			log.Println("fstype:", fstype)
			err = syscall.Mount(part, baseDir, fstype, 0, "")
			rplib.Checkerr(err)

			defer syscall.Unmount(baseDir, 0)
		}
	}

	log.Printf("save buildstamp")
	d, err := yaml.Marshal(&buildstamp)
	rplib.Checkerr(err)
	err = ioutil.WriteFile(filepath.Join(recoveryDir, utils.BuildStampFile), d, 0644)
	rplib.Checkerr(err)

	log.Printf("[deploy default efi bootdir]")

	rplib.Shellexec("rsync", "-aAX", "--exclude=*.snap", fmt.Sprintf("%s/image/system-boot/", tmpDir), recoveryDir)

	log.Printf("[create uEnv.txt]")
	rplib.Shellexec("cp", "-f", "data/uEnv.txt", fmt.Sprintf("%s/uEnv.txt", recoveryDir))

	os.Mkdir(fmt.Sprintf("%s/oemlog", recoveryDir), 0755)

	os.Mkdir(fmt.Sprintf("%s/recovery/", recoveryDir), 0755)
	log.Printf("[add config.yaml]")
	rplib.Shellexec("cp", "-f", "config.yaml", fmt.Sprintf("%s/recovery/", recoveryDir))
	log.Printf("[add folder bin/]")
	rplib.Shellexec("cp", "-r", "data/bin", fmt.Sprintf("%s/recovery/", recoveryDir))
	log.Printf("[add factory snaps folder: factory/]")
	rplib.Shellexec("cp", "-r", "data/factory", fmt.Sprintf("%s/recovery/", recoveryDir))
	log.Printf("[add folder assertions/]")
	rplib.Shellexec("cp", "-r", "data/assertions", fmt.Sprintf("%s/recovery/", recoveryDir))

	if configs.Yaml.Configs.OemPreinstHookDir != "" {
		log.Printf("[Create oem specific pre-install hook directory]")
		os.Mkdir(fmt.Sprintf("%s/recovery/factory/%s", recoveryDir, configs.Yaml.Configs.OemPreinstHookDir), 0755)
	}

	if configs.Yaml.Configs.OemPostinstHookDir != "" {
		log.Printf("[Create oem specific post-install hook directory]")
		os.Mkdir(fmt.Sprintf("%s/recovery/factory/%s", recoveryDir, configs.Yaml.Configs.OemPostinstHookDir), 0755)
	}

	if configs.Yaml.Recovery.SystembootImage != "" && configs.Yaml.Recovery.WritableImage != "" {
		log.Printf("Copy user provided system-boot (%s) and writable (%s) images to %s/recovery/factory directory\n",
			configs.Yaml.Recovery.SystembootImage, configs.Yaml.Recovery.WritableImage, recoveryDir)

		rplib.Shellexec("cp", configs.Yaml.Recovery.SystembootImage, fmt.Sprintf("%s/recovery/factory/", recoveryDir))
		rplib.Shellexec("cp", configs.Yaml.Recovery.WritableImage, fmt.Sprintf("%s/recovery/factory/", recoveryDir))
	} else {
		log.Printf("add system-data and writable tarball from base image")

		workingDir, err := os.Getwd()
		rplib.Checkerr(err)

		err = os.Chdir(fmt.Sprintf("%s/image/system-boot/", tmpDir))
		rplib.Checkerr(err)
		rplib.Shellexec("tar", "--xattrs", "-Jcpf", fmt.Sprintf("%s/recovery/factory/system-boot.tar.xz", recoveryDir), ".")

		err = os.Chdir(fmt.Sprintf("%s/image/writable/", tmpDir))
		rplib.Checkerr(err)
		rplib.Shellexec("tar", "--xattrs", "-Jcpf", fmt.Sprintf("%s/recovery/factory/writable.tar.xz", recoveryDir), ".")

		os.Chdir(workingDir)
	}

	// copy kernel, gadget, os snap
	kernelSnap := findSnap(fmt.Sprintf("%s/image/writable/system-data/var/lib/snapd/snaps/", tmpDir), configs.Yaml.Snaps.Kernel)
	rplib.Shellexec("cp", "-f", kernelSnap, fmt.Sprintf("%s/kernel.snap", recoveryDir))
	gadgetSnap := findSnap(fmt.Sprintf("%s/image/writable/system-data/var/lib/snapd/snaps/", tmpDir), configs.Yaml.Snaps.Gadget)
	rplib.Shellexec("cp", "-f", gadgetSnap, fmt.Sprintf("%s/gadget.snap", recoveryDir))
	osSnap := findSnap(fmt.Sprintf("%s/image/writable/system-data/var/lib/snapd/snaps/", tmpDir), configs.Yaml.Snaps.Os)
	rplib.Shellexec("cp", "-f", osSnap, fmt.Sprintf("%s/os.snap", recoveryDir))

	//Update uEnv.txt for os.snap/kernel.snap
	log.Printf("[Set os/kernel snap in uEnv.txt]")
	f, err := os.OpenFile(fmt.Sprintf("%s/uEnv.txt", recoveryDir), os.O_APPEND|os.O_WRONLY, 0644)
	rplib.Checkerr(err)
	defer f.Close()
	_, err = f.WriteString(fmt.Sprintf("snap_core=%s\n", path.Base(osSnap)))
	_, err = f.WriteString(fmt.Sprintf("snap_kernel=%s\n", path.Base(kernelSnap)))
	rplib.Checkerr(err)

	log.Printf("[setup initrd.img and vmlinuz]")
	initrdImagePath := fmt.Sprintf("%s/initrd.img", recoveryDir)
	setupInitrd(initrdImagePath, tmpDir)
}

func compressXZImage(imageFile string) {
	log.Printf("[compress image: %s.xz]", imageFile)
	rplib.Shellexec("xz", "-0", imageFile)
}

func printUsage() {
	fmt.Println("")
	fmt.Println("ubuntu-recovery-image")
	fmt.Println("[execute ubuntu-recovery-image in config folder]")
	fmt.Println("")
}

var configs rplib.ConfigRecovery

func main() {
	// Print version
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if "" == version {
		version = utils.Version
	}

	commitstampInt64, _ := strconv.ParseInt(commitstamp, 10, 64)
	var buildstamp = utils.BuildStamp{
		BuildDate: time.Now().UTC(),
		BuildTool: utils.ProjectInfo{
			Version:     version,
			Commit:      commit,
			CommitStamp: time.Unix(commitstampInt64, 0).UTC(),
		},
		BuildConfig: utils.ProjectInfo{
			Version:     utils.ReadVersionFromPackageJson(),
			Commit:      utils.GetGitSha(),
			CommitStamp: time.Unix(utils.CommitStamp(), 0).UTC(),
		},
	}
	log.Printf("Version: %v, Commit: %v, Commit date: %v\n", version, commit, time.Unix(commitstampInt64, 0).UTC())

	// Parse config.yaml
	var errBool bool
	configs, errBool = rplib.LoadYamlConfig("config.yaml")
	if errBool {
		fmt.Println("Error: parse config.yaml failed")
		os.Exit(1)
	}

	log.Printf("[Setup project for %s]", configs.Yaml.Project)

	// Create base image or recovery image or both according to 'recoverytype' field in config.yaml
	if configs.Yaml.Configs.RecoveryType == "base" || configs.Yaml.Configs.RecoveryType == "full" {
		createBaseImage()
		if configs.Yaml.Configs.RecoveryType == "base" {
			log.Printf("[Create base image %s only]", configs.Yaml.Configs.BaseImage)
			os.Exit(0)
		}
	} else if configs.Yaml.Configs.RecoveryType == "recovery" {
		log.Printf("[Base image is %s]", configs.Yaml.Configs.BaseImage)
	} else {
		fmt.Printf("Error: %q is not valid type.\n", configs.Yaml.Configs.RecoveryType)
		os.Exit(2)
	}

	// Check if base image exists
	if _, err := os.Stat(configs.Yaml.Configs.BaseImage); err != nil {
		fmt.Printf("Error: can not find base image: %s, please build base image first\n", configs.Yaml.Configs.BaseImage)
		os.Exit(2)
	}

	// Create recovery image if 'recoverytype' field is 'recovery' or 'full' in config.yaml
	recoveryNR := "1"

	log.Printf("[start create recovery image with skipxz options: %s.\n]", configs.Yaml.Debug.Xz)

	todayTime := time.Now()
	todayDate := fmt.Sprintf("%d%02d%02d", todayTime.Year(), todayTime.Month(), todayTime.Day())
	recoveryOutputFile := configs.Yaml.Project + "-" + todayDate + "-0.img"

	createRecoveryImage(recoveryNR, recoveryOutputFile, buildstamp)

	// Compress image to xz if 'xz' field is 'on' in config.yaml
	if configs.Yaml.Debug.Xz {
		compressXZImage(recoveryOutputFile)
	}
}
