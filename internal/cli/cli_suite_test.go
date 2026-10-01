package cli_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

// waythroughPath and fakelspPath are built once, for the specs that run
// waythrough as the separate processes an agent and a shared daemon are.
var waythroughPath, fakelspPath string

var _ = BeforeSuite(func() {
	var err error
	waythroughPath, err = gexec.Build("github.com/gustavofsantos/waythrough/cmd/waythrough")
	Expect(err).NotTo(HaveOccurred())
	fakelspPath, err = gexec.Build("github.com/gustavofsantos/waythrough/internal/lsp/fakelsp")
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	gexec.CleanupBuildArtifacts()
})

func TestCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CLI Suite")
}
