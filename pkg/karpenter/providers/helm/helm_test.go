package helm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/otiai10/copy"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"

	"github.com/weaveworks/eksctl/pkg/karpenter/providers/fakes"
)

var _ = Describe("HelmInstaller", func() {

	Context("AddRepo", func() {

		var (
			fakeURLGetter      *fakes.FakeURLGetter
			getters            getter.Providers
			tmp                string
			err                error
			installerUnderTest *Installer
		)

		BeforeEach(func() {
			tmp, err = os.MkdirTemp("", "helm-testing")
			Expect(err).NotTo(HaveOccurred())
			fakeURLGetter = &fakes.FakeURLGetter{}
			provider := getter.Provider{
				Schemes: []string{"http", "https"},
				New: func(options ...getter.Option) (getter.Getter, error) {
					return fakeURLGetter, nil
				},
			}
			getters = append(getters, provider)
			installerUnderTest = &Installer{
				Getters: getters,
				Settings: &cli.EnvSettings{
					RegistryConfig:   filepath.Join(tmp, "registry.json"),
					RepositoryConfig: filepath.Join(tmp, "repositories.yaml"),
					RepositoryCache:  tmp,
				},
			}
		})

		AfterEach(func() {
			_ = os.RemoveAll(tmp)
		})

		It("successfully creates the repo metadata on the configured temp location", func() {
			buffer, err := dummyIndexFile()
			Expect(err).NotTo(HaveOccurred())
			fakeURLGetter.GetReturns(buffer, nil)
			Expect(installerUnderTest.AddRepo("https://charts.karpenter.sh", "karpenter")).To(Succeed())
			content, err := os.ReadFile(filepath.Join(tmp, "repositories.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal(expectedRepositoryYaml))
		})
		When("the getter fails to retrieve the index file", func() {
			It("errors", func() {
				fakeURLGetter.GetReturns(nil, errors.New("nope"))
				Expect(installerUnderTest.AddRepo("https://charts.karpenter.sh", "karpenter")).
					To(MatchError(ContainSubstring("failed to download index file: nope")))
			})
		})
		When("the getter returns an invalid JSON", func() {
			It("errors", func() {
				buffer := bytes.NewBuffer([]byte("invalid"))
				fakeURLGetter.GetReturns(buffer, nil)
				Expect(installerUnderTest.AddRepo("https://charts.karpenter.sh", "karpenter")).
					To(MatchError(ContainSubstring("failed to download index file: error unmarshaling JSON")))
			})
		})
		When("the repository url is invalid", func() {
			It("errors", func() {
				Expect(installerUnderTest.AddRepo("%^&", "karpenter")).
					To(MatchError(ContainSubstring("invalid chart URL format: %^&")))
			})
		})
		When("there is no provider for the given scheme", func() {
			It("errors", func() {
				installer := Installer{
					Getters: nil,
					Settings: &cli.EnvSettings{
						RegistryConfig:   filepath.Join(tmp, "registry.json"),
						RepositoryConfig: filepath.Join(tmp, "repositories.yaml"),
						RepositoryCache:  tmp,
					},
				}
				Expect(installer.AddRepo("https://charts.karpenter.sh", "karpenter")).
					To(MatchError(ContainSubstring("failed to create new chart repository: could not find protocol handler for: ")))
			})
		})
	})

	Context("InstallChart", func() {

		var (
			fakeGetter         *fakes.FakeURLGetter
			getters            getter.Providers
			tmp                string
			err                error
			installerUnderTest *Installer
			values             map[string]interface{}
			actionConfig       *action.Configuration
		)

		BeforeEach(func() {
			tmp, err = os.MkdirTemp("", "helm-testing")
			Expect(err).NotTo(HaveOccurred())
			fakeGetter = &fakes.FakeURLGetter{}
			provider := getter.Provider{
				Schemes: []string{"http", "https"},
				New: func(options ...getter.Option) (getter.Getter, error) {
					return fakeGetter, nil
				},
			}
			getters = append(getters, provider)
			store := storage.Init(driver.NewMemory())
			actionConfig = &action.Configuration{
				Releases:     store,
				KubeClient:   &kubefake.PrintingKubeClient{Out: ioutil.Discard},
				Capabilities: chartutil.DefaultCapabilities,
				Log:          func(format string, v ...interface{}) {},
			}
			installerUnderTest = &Installer{
				Getters: getters,
				Settings: &cli.EnvSettings{
					RepositoryCache:  tmp,
					RepositoryConfig: filepath.Join(tmp, "repositories.yaml"),
					Debug:            true,
				},
				ActionConfig: actionConfig,
			}
			values = map[string]interface{}{
				"controller.clusterName":     "test-security-groups-labels",
				"controller.clusterEndpoint": "https://E2AB8AEA541E5A354CBBFACE368C534D.sk1.us-west-2.eks.amazonaws.com",
				"serviceAccount.create":      false,
				"defaultProvisioner.create":  false,
			}
		})

		AfterEach(func() {
			_ = os.RemoveAll(tmp)
		})

		It("can install a test chart", func() {
			// write out repo config
			Expect(os.WriteFile(filepath.Join(tmp, "repositories.yaml"), []byte(expectedRepositoryYaml), 0644)).To(Succeed())
			Expect(copy.Copy(filepath.Join("testdata", "karpenter-0.4.3.tgz"), filepath.Join(tmp, "karpenter-0.4.3.tgz"))).To(Succeed())
			Expect(copy.Copy(filepath.Join("testdata", "karpenter-index.yaml"), filepath.Join(tmp, "karpenter-index.yaml"))).To(Succeed())
			Expect(installerUnderTest.InstallChart(context.Background(), "karpenter", "karpenter/karpenter", "karpenter", "0.4.3", values)).To(Succeed())
		})
		When("locate chart is unable to find the requested chart", func() {
			It("errors", func() {
				Expect(installerUnderTest.InstallChart(context.Background(), "karpenter", "karpenter/karpenter", "karpenter", "0.4.3", values)).To(MatchError(ContainSubstring("repo karpenter not found")))
			})
		})
		When("the version is unknown", func() {
			It("errors", func() {
				Expect(os.WriteFile(filepath.Join(tmp, "repositories.yaml"), []byte(expectedRepositoryYaml), 0644)).To(Succeed())
				Expect(copy.Copy(filepath.Join("testdata", "karpenter-0.4.3.tgz"), filepath.Join(tmp, "karpenter-0.4.3.tgz"))).To(Succeed())
				Expect(copy.Copy(filepath.Join("testdata", "karpenter-index.yaml"), filepath.Join(tmp, "karpenter-index.yaml"))).To(Succeed())
				Expect(installerUnderTest.InstallChart(context.Background(), "karpenter", "karpenter/karpenter", "karpenter", "0.1.0", values)).To(MatchError(ContainSubstring("failed to locate chart: chart \"karpenter\" matching 0.1.0 not found in karpenter index. (try 'helm repo update'): no chart version found for karpenter-0.1.0")))
			})
		})
		When("repository is invalid", func() {
			It("errors", func() {
				Expect(os.WriteFile(filepath.Join(tmp, "repositories.yaml"), []byte("invalid\n"), 0644)).To(Succeed())
				Expect(installerUnderTest.InstallChart(context.Background(), "karpenter", "karpenter/karpenter", "karpenter", "0.1.0", values)).To(MatchError(ContainSubstring("error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type repo.File")))
			})
		})
		When("kube client fails to reach the cluster", func() {
			It("errors", func() {
				Expect(os.WriteFile(filepath.Join(tmp, "repositories.yaml"), []byte(expectedRepositoryYaml), 0644)).To(Succeed())
				Expect(copy.Copy(filepath.Join("testdata", "karpenter-0.4.3.tgz"), filepath.Join(tmp, "karpenter-0.4.3.tgz"))).To(Succeed())
				Expect(copy.Copy(filepath.Join("testdata", "karpenter-index.yaml"), filepath.Join(tmp, "karpenter-index.yaml"))).To(Succeed())
				fakeKube := &kubefake.FailingKubeClient{
					PrintingKubeClient: kubefake.PrintingKubeClient{},
					CreateError:        errors.New("nope"),
				}
				actionConfig.KubeClient = fakeKube
				installerUnderTest.ActionConfig = actionConfig
				Expect(installerUnderTest.InstallChart(context.Background(), "karpenter", "karpenter/karpenter", "karpenter", "0.4.3", values)).To(MatchError(ContainSubstring("failed to install chart: failed to install CRD crds/karpenter.sh_provisioners.yaml: nope")))
			})
		})
	})
})

func dummyIndexFile() (*bytes.Buffer, error) {
	index := &repo.IndexFile{
		APIVersion: "v1",
		Generated:  time.Date(2021, 1, 1, 1, 1, 1, 1, time.UTC),
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	return bytes.NewBuffer(indexBytes), nil
}

var expectedRepositoryYaml = `apiVersion: ""
generated: "0001-01-01T00:00:00Z"
repositories:
- caFile: ""
  certFile: ""
  insecure_skip_tls_verify: false
  keyFile: ""
  name: karpenter
  pass_credentials_all: false
  password: ""
  url: https://charts.karpenter.sh
  username: ""
`
