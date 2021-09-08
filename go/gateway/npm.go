package gateway

import (
	"encoding/json"
	"fmt"
	"github.com/hashicorp/go-version"
	"go.uber.org/config"
	"go.uber.org/zap"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"
)

type NpmGatewayConfig struct {
	RegistryURL string `yaml:"registry_url"`
	Authorization string `yaml:"authorization"`
}

type NpmGateway interface {
	DownloadPackage(name, version string) (packageTarFile *os.File, err error)
}

type npmGateway struct {
	NpmGatewayConfig
	logger *zap.Logger
	url *url.URL
	httpClient *http.Client
}

type NpmDistInfo struct {
	Tarball string `json:"tarball"`
}

type NpmVersionInfo struct {
	Dist NpmDistInfo `json:"dist"`
}

type NpmPackageInfo struct {
	Versions map[string]NpmVersionInfo `json:"versions"`
}

func (n *npmGateway) addAuthorizationToRequest(req *http.Request) {
	bearerAuth := fmt.Sprintf("Bearer %s", n.Authorization)
	req.Header.Add("Authorization", bearerAuth)
}

func (n *npmGateway) getPackageURL(packageName string) *url.URL {
	baseURL, _ := url.Parse(n.url.String())
	baseURL.Path = packageName
	return baseURL
}

func NewNpmGateway(logger *zap.Logger, provider config.Provider) NpmGateway {
	var (
		gatewayConfig NpmGatewayConfig
	)

	err := provider.Get("npm_gateway").Populate(&gatewayConfig)
	if err != nil {
		panic(err)
	}

	parsedUrl, err := url.Parse(gatewayConfig.RegistryURL)
	if err != nil {
		panic(err)
	}
	parsedUrl.Scheme = "https"

	httpClient := &http.Client{
		Timeout: time.Second * 10,
	}

	return &npmGateway{
		NpmGatewayConfig: gatewayConfig,
		logger: logger,
		url: parsedUrl,
		httpClient: httpClient,
	}
}

func (n *npmGateway) findPackageVersionTar(name, packageVersion string) (tarUrl string, err error) {
	n.logger.Debug(
		"downloading package from npm",
		zap.String("name", name),
		zap.String("packageVersion", packageVersion),
	)

	packageURL := n.getPackageURL(name)

	req, err := http.NewRequest("GET", packageURL.String(), nil)
	if err != nil {
		n.logger.Error("", zap.Error(err))
		return
	}
	n.addAuthorizationToRequest(req)

	// Get the data
	resp, err := n.httpClient.Do(req)
	if err != nil {
		n.logger.Error("", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		n.logger.Error("", zap.Error(err))
		return
	}

	var npmPackgeInfo NpmPackageInfo
	err = json.Unmarshal(body, &npmPackgeInfo)
	if err != nil {
		n.logger.Error("", zap.Error(err))
		return
	}

	var (
		versions []*version.Version
		strVersions []string
		semverVersion *version.Version
	)
	for npmPackageVersion, _ := range npmPackgeInfo.Versions {
		semverVersion, err = version.NewVersion(npmPackageVersion)
		if err != nil {
			return
		}
		strVersions = append(strVersions, npmPackageVersion)
		versions = append(versions, semverVersion)
	}

	sort.Sort(sort.Reverse(version.Collection(versions)))

	versionConstraints, err := version.NewConstraint(fmt.Sprintf("~> %s", packageVersion))
	if err != nil {
		return
	}

	var latestPackageVersion string
	for _, npmPackageVersion := range versions {
		if versionConstraints.Check(npmPackageVersion) {
			latestPackageVersion = npmPackageVersion.String()
			break
		}
	}

	if latestPackageVersion == "" {
		err = fmt.Errorf("unable to find acceptable version for provided: %s", packageVersion)
		n.logger.Error(
			"unable to find acceptable version",
			zap.String("packageVersion", packageVersion),
			zap.Strings("versions", strVersions),
		)
		return
	}

	packageVersionInfo, ok := npmPackgeInfo.Versions[latestPackageVersion]
	if !ok {
		err = fmt.Errorf("unable to location packageVersion %s for package %s", packageVersion, name)
		n.logger.Error("", zap.Error(err))
		return
	}
	tarUrl = packageVersionInfo.Dist.Tarball
	return
}

func (n *npmGateway) downloadPackageTar(packageTarURL string) (packageTarFile *os.File, err error) {
	req, err := http.NewRequest("GET", packageTarURL, nil)
	if err != nil {
		n.logger.Error("", zap.Error(err))
		return
	}
	n.addAuthorizationToRequest(req)

	// Get the data
	resp, err := n.httpClient.Do(req)
	if err != nil {
		n.logger.Error("", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	packageTarFile, err = ioutil.TempFile(os.TempDir(), "*.tar")
	if err != nil {
		n.logger.Error("", zap.Error(err))
		return
	}

	// Write the body to file
	_, err = io.Copy(packageTarFile, resp.Body)
	return
}

func (n *npmGateway) DownloadPackage(name, version string) (packageTarFile *os.File, err error) {
	tarUrl, err := n.findPackageVersionTar(name, version)
	if err != nil {
		return
	}

	return n.downloadPackageTar(tarUrl)
}