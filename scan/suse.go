package scan

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"

	"github.com/future-architect/vuls/config"
	"github.com/future-architect/vuls/models"
	"github.com/future-architect/vuls/util"
)

// inherit OsTypeInterface
type suse struct {
	redhatBase
}

// NewRedhat is constructor
func newSUSE(c config.ServerInfo) *suse {
	r := &suse{
		redhatBase: redhatBase{
			base: base{
				osPackages: osPackages{
					Packages:  models.Packages{},
					VulnInfos: models.VulnInfos{},
				},
			},
		},
	}
	r.log = util.NewCustomLogger(c)
	r.setServerInfo(c)
	return r
}

// https://github.com/mizzy/specinfra/blob/master/lib/specinfra/helper/detect_os/suse.rb
func detectSUSE(c config.ServerInfo) (bool, osTypeInterface) {
	s := newSUSE(c)
	if r := exec(c, "ls /etc/os-release", noSudo); r.isSuccess() {
		if r := exec(c, "zypper -V", noSudo); r.isSuccess() {
			if r := exec(c, "cat /etc/os-release", noSudo); r.isSuccess() {
				name, ver := s.parseOSRelease(r.Stdout)
				s.setDistro(name, ver)
				return true, s
			}
		}
	} else if r := exec(c, "ls /etc/SuSE-release", noSudo); r.isSuccess() {
		if r := exec(c, "zypper -V", noSudo); r.isSuccess() {
			if r := exec(c, "cat /etc/SuSE-release", noSudo); r.isSuccess() {
				re := regexp.MustCompile(`openSUSE (\d+\.\d+|\d+)`)
				result := re.FindStringSubmatch(strings.TrimSpace(r.Stdout))
				if len(result) == 2 {
					//TODO check opensuse or opensuse.leap
					s.setDistro(config.OpenSUSE, result[1])
					return true, s
				}

				re = regexp.MustCompile(`VERSION = (\d+)`)
				result = re.FindStringSubmatch(strings.TrimSpace(r.Stdout))
				if len(result) == 2 {
					version := result[1]
					re = regexp.MustCompile(`PATCHLEVEL = (\d+)`)
					result = re.FindStringSubmatch(strings.TrimSpace(r.Stdout))
					if len(result) == 2 {
						s.setDistro(config.SUSEEnterpriseServer,
							fmt.Sprintf("%s.%s", version, result[1]))
						return true, s
					}
				}
				util.Log.Warnf("Failed to parse SUSE Linux version: %s", r)
				return true, s
			}
		}
	}
	util.Log.Debugf("Not SUSE Linux. servername: %s", c.ServerName)
	return false, s
}

func (o *suse) parseOSRelease(content string) (name string, ver string) {
	if strings.Contains(content, "ID=opensuse") {
		//TODO check opensuse or opensuse.leap
		name = config.OpenSUSE
	} else if strings.Contains(content, `NAME="SLES"`) {
		name = config.SUSEEnterpriseServer
	} else if strings.Contains(content, `NAME="SLES_SAP"`) {
		name = config.SUSEEnterpriseServer
	} else {
		util.Log.Warnf("Failed to parse SUSE edition: %s", content)
		return "unknown", "unknown"
	}

	re := regexp.MustCompile(`VERSION_ID=\"(.+)\"`)
	result := re.FindStringSubmatch(strings.TrimSpace(content))
	if len(result) != 2 {
		util.Log.Warnf("Failed to parse SUSE Linux version: %s", content)
		return "unknown", "unknown"
	}
	return name, result[1]
}

func (o *suse) checkScanMode() error {
	return nil
}

func (o *suse) checkDeps() error {
	o.log.Infof("Dependencies... No need")
	return nil
}

func (o *suse) checkIfSudoNoPasswd() error {
	// SUSE doesn't need root privilege
	o.log.Infof("sudo ... No need")
	return nil
}

func (o *suse) scanPackages() error {
	installed, err := o.scanInstalledPackages()
	if err != nil {
		o.log.Errorf("Failed to scan installed packages: %s", err)
		return err
	}

	rebootRequired, err := o.rebootRequired()
	if err != nil {
		o.log.Errorf("Failed to detect the kernel reboot required: %s", err)
		return err
	}
	o.Kernel.RebootRequired = rebootRequired
	if o.getServerInfo().Mode.IsOffline() {
		o.Packages = installed
		return nil
	}

	updatable, err := o.scanUpdatablePackages()
	if err != nil {
		o.log.Errorf("Failed to scan updatable packages: %s", err)
		return err
	}
	installed.MergeNewVersion(updatable)
	o.Packages = installed

	return nil
}

func (o *suse) rebootRequired() (bool, error) {
	r := o.exec("rpm -q --last kernel-default", noSudo)
	if !r.isSuccess() {
		o.log.Warnf("Failed to detect the last installed kernel : %v", r)
		// continue scanning
		return false, nil
	}
	stdout := strings.Fields(r.Stdout)[0]
	return !strings.Contains(stdout, strings.TrimSuffix(o.Kernel.Release, "-default")), nil
}

func (o *suse) scanUpdatablePackages() (models.Packages, error) {
	cmd := "zypper -q lu"
	if o.hasZypperColorOption() {
		cmd = "zypper -q --no-color lu"
	}
	r := o.exec(cmd, noSudo)
	if !r.isSuccess() {
		return nil, fmt.Errorf("Failed to scan updatable packages: %v", r)
	}
	return o.parseZypperLULines(r.Stdout)
}

func (o *suse) parseZypperLULines(stdout string) (models.Packages, error) {
	updatables := models.Packages{}
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Index(line, "S | Repository") != -1 ||
			strings.Index(line, "--+----------------") != -1 {
			continue
		}
		pack, err := o.parseZypperLUOneLine(line)
		if err != nil {
			return nil, err
		}
		updatables[pack.Name] = *pack
	}
	return updatables, nil
}

func (o *suse) parseZypperLUOneLine(line string) (*models.Package, error) {
	fs := strings.Fields(line)
	if len(fs) != 11 {
		return nil, fmt.Errorf("zypper -q lu Unknown format: %s", line)
	}
	available := strings.Split(fs[8], "-")
	return &models.Package{
		Name:       fs[4],
		NewVersion: available[0],
		NewRelease: available[1],
		Arch:       fs[10],
	}, nil
}

func (o *suse) hasZypperColorOption() bool {
	cmd := "zypper --help | grep color"
	r := o.exec(util.PrependProxyEnv(cmd), noSudo)
	return len(r.Stdout) > 0
}
