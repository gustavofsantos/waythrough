package lsp_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var fakelspPath string

var _ = BeforeSuite(func() {
	var err error
	fakelspPath, err = gexec.Build("github.com/gustavofsantos/waythrough/internal/lsp/fakelsp")
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	gexec.CleanupBuildArtifacts()
})

func TestLSP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LSP Suite")
}
