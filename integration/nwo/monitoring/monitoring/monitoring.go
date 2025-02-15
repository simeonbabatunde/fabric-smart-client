/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package monitoring

import (
	"bufio"
	"context"
	"fmt"
	docker "github.com/fsouza/go-dockerclient"
	"github.com/hyperledger-labs/fabric-smart-client/integration/nwo/api"
	"github.com/hyperledger-labs/fabric-smart-client/integration/nwo/fabric"
	"github.com/hyperledger-labs/fabric-smart-client/integration/nwo/fsc"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"

	"github.com/hyperledger-labs/fabric-smart-client/integration/nwo/fabric/helpers"
	nnetwork "github.com/hyperledger-labs/fabric-smart-client/integration/nwo/fabric/network"
	"github.com/hyperledger-labs/fabric-smart-client/platform/view/services/flogging"
)

const (
	PrometheusImg = "prom/prometheus:latest"
	GrafanaImg    = "grafana/grafana:latest"
)

var RequiredImages = []string{
	PrometheusImg,
	GrafanaImg,
}

var logger = flogging.MustGetLogger("integration.nwo.fabric.monitoring")

type Platform interface {
	HyperledgerExplorer() bool
	GetContext() api.Context
	ConfigDir() string
	DockerClient() *docker.Client
	NetworkID() string
	PrometheusGrafana() bool
	PrometheusPort() int
	GrafanaPort() int
}

type Extension struct {
	platform Platform
}

func NewExtension(platform Platform) *Extension {
	return &Extension{
		platform: platform,
	}
}

func (n *Extension) CheckTopology() {
	if !n.platform.PrometheusGrafana() {
		return
	}

	helpers.AssertImagesExist(RequiredImages...)

	return
}

func (n *Extension) GenerateArtifacts() {
	if !n.platform.PrometheusGrafana() {
		return
	}

	// Generate and store prometheus config
	prometheusConfig := Prometheus{
		Global: Global{
			ScrapeInterval:     "15s",
			EvaluationInterval: "15s",
		},
		ScrapeConfigs: []ScrapeConfig{},
	}

	n.prometheusScrape(&prometheusConfig)
	n.fabricScrapes(&prometheusConfig)
	n.fscScrapes(&prometheusConfig)

	// store prometheus configuration
	configYAML, err := yaml.Marshal(prometheusConfig)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.MkdirAll(n.configFileDir(), 0o755)).NotTo(HaveOccurred())
	Expect(os.MkdirAll(n.prometheusConfigDir(), 0o755)).NotTo(HaveOccurred())
	Expect(ioutil.WriteFile(n.prometheusConfigFilePath(), configYAML, 0o644)).NotTo(HaveOccurred())

	// store grafana configuration
	for _, dir := range n.grafanaDirPaths() {
		Expect(os.MkdirAll(dir, 0o755)).NotTo(HaveOccurred())
	}
	Expect(ioutil.WriteFile(n.grafanaProvisioningDashboardFilePath(), []byte(DashboardTemplate), 0o644)).NotTo(HaveOccurred())
	Expect(ioutil.WriteFile(n.grafanaProvisioningDatasourceFilePath(), []byte(DatasourceTemplate), 0o644)).NotTo(HaveOccurred())
	Expect(ioutil.WriteFile(n.grafanaDashboardFabricBackendFilePath(), []byte(DashboardFabricBackendTemplate), 0o644)).NotTo(HaveOccurred())
	Expect(ioutil.WriteFile(n.grafanaDashboardFabricBusinessFilePath(), []byte(DashboardFabricBusinessTemplate), 0o644)).NotTo(HaveOccurred())
}

func (n *Extension) PostRun(bool) {
	if !n.platform.PrometheusGrafana() {
		return
	}

	logger.Infof("Run Prometheus...")
	n.dockerPrometheus()
	logger.Infof("Run Prometheus..done!")
	time.Sleep(30 * time.Second)
	logger.Infof("Run Grafana...")
	n.dockerGrafana()
	logger.Infof("Run Grafana...done!")
}

func (n *Extension) fabricScrapes(p *Prometheus) {
	for _, platform := range n.platform.GetContext().PlatformsByType(fabric.TopologyName) {
		fabricPlatform := platform.(*fabric.Platform)

		osc := ScrapeConfig{
			JobName: "orderers",
			Scheme:  "http",
			StaticConfigs: []StaticConfig{
				{
					Targets: []string{},
				},
			},
		}
		for _, orderer := range fabricPlatform.Orderers() {
			osc.StaticConfigs[0].Targets = append(osc.StaticConfigs[0].Targets, orderer.OperationAddress)
		}
		p.ScrapeConfigs = append(p.ScrapeConfigs, osc)
		for _, org := range fabricPlatform.PeerOrgs() {
			sc := ScrapeConfig{
				JobName: "Peers in " + org.Name,
				Scheme:  "http",
				StaticConfigs: []StaticConfig{
					{
						Targets: []string{},
					},
				},
			}
			for _, peer := range fabricPlatform.PeersByOrg("", org.Name, false) {
				sc.StaticConfigs[0].Targets = append(sc.StaticConfigs[0].Targets, peer.OperationAddress)
			}
			p.ScrapeConfigs = append(p.ScrapeConfigs, sc)
		}
	}
}

func (n *Extension) fscScrapes(p *Prometheus) {
	platform := n.platform.GetContext().PlatformsByType(fsc.TopologyName)[0].(*fsc.Platform)
	for _, peer := range platform.Peers {
		replace := func(s string) string {
			return strings.Replace(s, n.fscCryptoDir(), "/etc/prometheus/fsc/crypto", -1)
		}
		sc := ScrapeConfig{
			JobName: "FSC Node " + peer.Name,
			Scheme:  "https",
			StaticConfigs: []StaticConfig{
				{
					Targets: []string{platform.OperationAddress(peer)},
				},
			},
			TLSConfig: &TLSConfig{
				CAFile:             replace(platform.NodeLocalTLSDir(peer) + "/ca.crt"),
				CertFile:           replace(platform.NodeLocalTLSDir(peer) + "/server.crt"),
				KeyFile:            replace(platform.NodeLocalTLSDir(peer) + "/server.key"),
				ServerName:         "",
				InsecureSkipVerify: true,
			},
		}
		p.ScrapeConfigs = append(p.ScrapeConfigs, sc)
	}
}

func (n *Extension) prometheusScrape(p *Prometheus) {
	p.ScrapeConfigs = append(p.ScrapeConfigs,
		ScrapeConfig{
			JobName: "prometheus",
			Scheme:  "http",
			StaticConfigs: []StaticConfig{
				{
					Targets: []string{"prometheus:" + strconv.Itoa(n.platform.PrometheusPort())},
				},
			},
		},
	)
}

func (n *Extension) dockerPrometheus() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	Expect(err).ToNot(HaveOccurred())

	net, err := n.platform.DockerClient().NetworkInfo(n.platform.NetworkID())
	Expect(err).ToNot(HaveOccurred())

	containerName := n.platform.NetworkID() + "-prometheus"

	port := strconv.Itoa(n.platform.PrometheusPort())
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Hostname: "prometheus",
		Image:    "prom/prometheus:latest",
		Tty:      true,
		ExposedPorts: nat.PortSet{
			nat.Port(port + "/tcp"): struct{}{},
		},
	}, &container.HostConfig{
		ExtraHosts:    []string{fmt.Sprintf("fabric:%s", nnetwork.LocalIP(n.platform.DockerClient(), n.platform.NetworkID()))},
		RestartPolicy: container.RestartPolicy{Name: "always"},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: n.prometheusConfigFilePath(),
				Target: "/etc/prometheus/prometheus.yml",
			},
			{
				Type:   mount.TypeBind,
				Source: n.fscCryptoDir(),
				Target: "/etc/prometheus/fsc/crypto",
			},
		},
		PortBindings: nat.PortMap{
			nat.Port(port + "/tcp"): []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: port,
				},
			},
		},
	}, &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			n.platform.NetworkID(): {
				NetworkID: net.ID,
			},
		},
	}, nil, containerName,
	)
	Expect(err).ToNot(HaveOccurred())

	err = cli.NetworkConnect(context.Background(), n.platform.NetworkID(), resp.ID, &network.EndpointSettings{
		NetworkID: n.platform.NetworkID(),
	})
	Expect(err).ToNot(HaveOccurred())

	Expect(cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})).ToNot(HaveOccurred())

	dockerLogger := flogging.MustGetLogger("prometheus.container")
	go func() {
		reader, err := cli.ContainerLogs(context.Background(), resp.ID, types.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Timestamps: false,
		})
		Expect(err).ToNot(HaveOccurred())
		defer reader.Close()

		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			dockerLogger.Debugf("%s", scanner.Text())
		}
	}()

}

func (n *Extension) dockerGrafana() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	Expect(err).ToNot(HaveOccurred())

	net, err := n.platform.DockerClient().NetworkInfo(n.platform.NetworkID())
	Expect(err).ToNot(HaveOccurred())

	containerName := n.platform.NetworkID() + "-grafana"

	port := strconv.Itoa(n.platform.GrafanaPort())
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Hostname: "grafana",
		Image:    "grafana/grafana:latest",
		Tty:      false,
		Env: []string{
			"GF_AUTH_PROXY_ENABLED=true",
			"GF_PATHS_PROVISIONING=/var/lib/grafana/provisioning/",
		},
		ExposedPorts: nat.PortSet{
			nat.Port(port + "/tcp"): struct{}{},
		},
	}, &container.HostConfig{
		Links: []string{n.platform.NetworkID() + "-prometheus"},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: n.grafanaProvisioningDirPath(),
				Target: "/var/lib/grafana/provisioning/",
			},
			{
				Type:   mount.TypeBind,
				Source: n.grafanaDashboardDirPath(),
				Target: "/var/lib/grafana/dashboards/",
			},
		},
		PortBindings: nat.PortMap{
			nat.Port(port + "/tcp"): []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: port,
				},
			},
		},
	},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				n.platform.NetworkID(): {
					NetworkID: net.ID,
				},
			},
		}, nil, containerName,
	)
	Expect(err).ToNot(HaveOccurred())

	err = cli.NetworkConnect(context.Background(), n.platform.NetworkID(), resp.ID, &network.EndpointSettings{
		NetworkID: n.platform.NetworkID(),
	})
	Expect(err).ToNot(HaveOccurred())

	Expect(cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})).ToNot(HaveOccurred())
	time.Sleep(3 * time.Second)

	dockerLogger := flogging.MustGetLogger("grafana.container")
	go func() {
		reader, err := cli.ContainerLogs(context.Background(), resp.ID, types.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Timestamps: false,
		})
		Expect(err).ToNot(HaveOccurred())
		defer reader.Close()

		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			dockerLogger.Debugf("%s", scanner.Text())
		}
	}()
}

func (n *Extension) configFileDir() string {
	return filepath.Join(
		n.platform.ConfigDir(),
	)
}

func (n *Extension) grafanaDirPaths() []string {
	return []string{
		filepath.Join(
			n.configFileDir(),
			"grafana",
			"dashboards",
		),
		filepath.Join(
			n.configFileDir(),
			"grafana",
			"provisioning",
		),
		filepath.Join(
			n.configFileDir(),
			"grafana",
			"provisioning",
			"dashboards",
		),
		filepath.Join(
			n.configFileDir(),
			"grafana",
			"provisioning",
			"datasources",
		),
		filepath.Join(
			n.configFileDir(),
			"grafana",
			"provisioning",
			"notifiers",
		),
	}
}

func (n *Extension) grafanaProvisioningDirPath() string {
	return filepath.Join(
		n.configFileDir(),
		"grafana",
		"provisioning",
	)
}

func (n *Extension) grafanaProvisioningDashboardFilePath() string {
	return filepath.Join(
		n.configFileDir(),
		"grafana",
		"provisioning",
		"dashboards",
		"dashboard.yaml",
	)
}

func (n *Extension) grafanaProvisioningDatasourceFilePath() string {
	return filepath.Join(
		n.configFileDir(),
		"grafana",
		"provisioning",
		"datasources",
		"datasource.yaml",
	)
}

func (n *Extension) grafanaDashboardDirPath() string {
	return filepath.Join(
		n.configFileDir(),
		"grafana",
		"dashboards",
	)
}

func (n *Extension) grafanaDashboardFabricBackendFilePath() string {
	return filepath.Join(
		n.configFileDir(),
		"grafana",
		"dashboards",
		"fabric_backed.json",
	)
}

func (n *Extension) grafanaDashboardFabricBusinessFilePath() string {
	return filepath.Join(
		n.configFileDir(),
		"grafana",
		"dashboards",
		"fabric_business.json",
	)
}

func (n *Extension) prometheusConfigDir() string {
	return filepath.Join(n.configFileDir(), "prometheus")
}

func (n *Extension) prometheusConfigFilePath() string {
	return filepath.Join(n.configFileDir(), "prometheus", "prometheus.yml")
}

func (n *Extension) fscCryptoDir() string {
	return n.platform.GetContext().PlatformsByType(fsc.TopologyName)[0].(*fsc.Platform).CryptoPath()
}
