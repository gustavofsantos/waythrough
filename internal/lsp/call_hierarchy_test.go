package lsp_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

var _ = Describe("call hierarchy", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
	})

	AfterEach(func() { cancel() })

	When("an internal caller supplies an unknown direction value", func() {
		It("rejects the invalid typed value before resolving a server", func() {
			manager := lsp.NewManager(GinkgoT().TempDir(), nil)

			_, err := manager.CallHierarchy(
				ctx, "missing", "main.fake", 1, 1, lsp.CallDirection("sideways"))
			Expect(err).To(MatchError(
				`call hierarchy direction must be incoming or outgoing, got "sideways"`))
		})
	})

	When("the prepared function has an incoming caller", func() {
		It("returns the root, caller, and every call site", func() {
			manager, file := fakeManager(ctx, "target()",
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[{
					"name":"target","kind":12,"detail":"func target()",
					"uri":"file:///root/main.fake",
					"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":8}},
					"selectionRange":{
						"start":{"line":0,"character":0},
						"end":{"line":0,"character":6}
					},
					"data":{"opaque":"preserve me"}
				}]`,
				"-incoming-calls="+`[{
					"from":{
						"name":"caller","kind":12,"detail":"func caller()",
						"uri":"file:///root/caller.fake",
						"range":{"start":{"line":3,"character":0},"end":{"line":5,"character":1}},
						"selectionRange":{
							"start":{"line":3,"character":5},
							"end":{"line":3,"character":11}
						}
					},
					"fromRanges":[
						{"start":{"line":4,"character":1},"end":{"line":4,"character":7}},
						{"start":{"line":5,"character":1},"end":{"line":5,"character":7}}
					]
				}]`)

			hierarchies, err := manager.CallHierarchy(ctx, "fake", file, 1, 1, "incoming")
			Expect(err).NotTo(HaveOccurred())
			Expect(hierarchies).To(Equal([]lsp.CallHierarchy{{
				Symbol: lsp.Symbol{
					Name: "target", Kind: "function", Detail: "func target()",
					Location: lsp.Location{File: "/root/main.fake", Line: 1, Column: 1},
				},
				Calls: []lsp.Call{{
					Symbol: lsp.Symbol{
						Name: "caller", Kind: "function", Detail: "func caller()",
						Location: lsp.Location{File: "/root/caller.fake", Line: 4, Column: 6},
					},
					CallSites: []lsp.Location{
						{File: "/root/caller.fake", Line: 5, Column: 2},
						{File: "/root/caller.fake", Line: 6, Column: 2},
					},
				}},
			}}))
		})
	})

	When("the prepared function has an outgoing callee", func() {
		It("returns the callee and locates the call site in the root", func() {
			manager, file := fakeManager(ctx, "root()",
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[{
					"name":"root","kind":12,"uri":"file:///root/main.fake",
					"range":{"start":{"line":0,"character":0},"end":{"line":2,"character":1}},
					"selectionRange":{
						"start":{"line":0,"character":5},
						"end":{"line":0,"character":9}
					}
				}]`,
				"-outgoing-calls="+`[{
					"to":{
						"name":"callee","kind":6,"detail":"func callee()",
						"uri":"file:///root/callee.fake",
						"range":{"start":{"line":6,"character":0},"end":{"line":8,"character":1}},
						"selectionRange":{
							"start":{"line":6,"character":5},
							"end":{"line":6,"character":11}
						}
					},
					"fromRanges":[{"start":{"line":1,"character":1},"end":{"line":1,"character":7}}]
				}]`)

			hierarchies, err := manager.CallHierarchy(ctx, "fake", file, 1, 6, "outgoing")
			Expect(err).NotTo(HaveOccurred())
			Expect(hierarchies).To(Equal([]lsp.CallHierarchy{{
				Symbol: lsp.Symbol{
					Name: "root", Kind: "function",
					Location: lsp.Location{File: "/root/main.fake", Line: 1, Column: 6},
				},
				Calls: []lsp.Call{{
					Symbol: lsp.Symbol{
						Name: "callee", Kind: "method", Detail: "func callee()",
						Location: lsp.Location{File: "/root/callee.fake", Line: 7, Column: 6},
					},
					CallSites: []lsp.Location{{File: "/root/main.fake", Line: 2, Column: 2}},
				}},
			}}))
		})
	})

	When("the server returns callers and sites in an arbitrary order", func() {
		It("orders calls and call sites by source location", func() {
			manager, file := fakeManager(ctx, "target()",
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[{
					"name":"target","kind":12,"uri":"file:///root/main.fake",
					"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},
					"selectionRange":{
						"start":{"line":0,"character":0},
						"end":{"line":0,"character":1}
					}
				}]`,
				"-incoming-calls="+`[
					{
						"from":{
							"name":"caller-z","kind":12,"uri":"file:///root/z.fake",
							"range":{
								"start":{"line":8,"character":0},
								"end":{"line":8,"character":1}
							},
							"selectionRange":{
								"start":{"line":8,"character":0},
								"end":{"line":8,"character":1}
							}
						},
						"fromRanges":[
							{"start":{"line":9,"character":4},"end":{"line":9,"character":5}},
							{"start":{"line":9,"character":1},"end":{"line":9,"character":2}}
						]
					},
					{
						"from":{
							"name":"caller-a","kind":12,"uri":"file:///root/a.fake",
							"range":{
								"start":{"line":1,"character":0},
								"end":{"line":1,"character":1}
							},
							"selectionRange":{
								"start":{"line":1,"character":0},
								"end":{"line":1,"character":1}
							}
						},
						"fromRanges":[]
					}
				]`)

			hierarchies, err := manager.CallHierarchy(ctx, "fake", file, 1, 1, "incoming")
			Expect(err).NotTo(HaveOccurred())
			Expect([]string{
				hierarchies[0].Calls[0].Symbol.Name,
				hierarchies[0].Calls[1].Symbol.Name,
			}).To(Equal([]string{"caller-a", "caller-z"}))
			Expect(hierarchies[0].Calls[1].CallSites).To(Equal([]lsp.Location{
				{File: "/root/z.fake", Line: 10, Column: 2},
				{File: "/root/z.fake", Line: 10, Column: 5},
			}))
		})
	})

	When("the server advertises call hierarchy with an options object", func() {
		It("accepts the capability and prepares the position", func() {
			manager, file := fakeManager(ctx, "target()", "-call-hierarchy-options")

			hierarchies, err := manager.CallHierarchy(ctx, "fake", file, 1, 1, "incoming")
			Expect(err).NotTo(HaveOccurred())
			Expect(hierarchies).To(BeEmpty())
		})
	})

	When("readiness and hierarchy work need different time bounds", func() {
		It("does not apply the readiness timeout to the hierarchy operation", func() {
			root := GinkgoT().TempDir()
			file := filepath.Join(root, "main.fake")
			Expect(os.WriteFile(file, []byte("target()"), 0o644)).To(Succeed())
			entry := fakeEntry(
				"-call-hierarchy",
				"-call-hierarchy-roots-count=1",
				"-call-hierarchy-delay=50ms")
			manager := lsp.NewManager(root, []config.LanguageServer{entry},
				lsp.WithToolCallTimeout(20*time.Millisecond),
				lsp.WithCallHierarchyTimeout(time.Second))
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())

			_, err := manager.CallHierarchy(ctx, "fake", file, 1, 1, "incoming")
			Expect(err).NotTo(HaveOccurred())
		})

		It("cancels hierarchy work at its own operation deadline", func() {
			root := GinkgoT().TempDir()
			file := filepath.Join(root, "main.fake")
			Expect(os.WriteFile(file, []byte("target()"), 0o644)).To(Succeed())
			entry := fakeEntry(
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[{
					"name":"target","kind":12,"uri":"file:///root/main.fake",
					"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},
					"selectionRange":{
						"start":{"line":0,"character":0},
						"end":{"line":0,"character":1}
					}
				}]`,
				"-call-hierarchy-delay=100ms")
			manager := lsp.NewManager(root, []config.LanguageServer{entry},
				lsp.WithCallHierarchyTimeout(20*time.Millisecond))
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())

			_, err := manager.CallHierarchy(ctx, "fake", file, 1, 1, "incoming")
			Expect(err).To(MatchError(ContainSubstring("deadline exceeded")))
		})
	})

	When("a restart begins while a directed response is in flight", func() {
		It("rejects the response from the retiring server attempt", func() {
			requestLog := filepath.Join(GinkgoT().TempDir(), "requests.log")
			root := GinkgoT().TempDir()
			file := filepath.Join(root, "main.fake")
			Expect(os.WriteFile(file, []byte("target()"), 0o644)).To(Succeed())
			entry := fakeEntry(
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[{
					"name":"target","kind":12,"uri":"file:///root/main.fake",
					"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},
					"selectionRange":{
						"start":{"line":0,"character":0},
						"end":{"line":0,"character":1}
					}
				}]`,
				"-call-hierarchy-async",
				"-call-hierarchy-delay=50ms",
				"-ignore-exit",
				"-request-log="+requestLog)
			manager := lsp.NewManager(root, []config.LanguageServer{entry},
				lsp.WithShutdownGrace(200*time.Millisecond))
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())

			hierarchyResult := make(chan error, 1)
			go func() {
				_, err := manager.CallHierarchy(ctx, "fake", file, 1, 1, "incoming")
				hierarchyResult <- err
			}()
			Eventually(func() []string { return logLines(requestLog) }).Should(
				ContainElement("callHierarchy/incomingCalls"))

			restartResult := make(chan error, 1)
			go func() { restartResult <- manager.Restart(ctx, "fake") }()
			Eventually(func() []string { return logLines(requestLog) }).Should(
				ContainElement("shutdown"), "restart must begin before the directed response returns")

			Eventually(hierarchyResult).Should(Receive(MatchError(
				ContainSubstring("restarted while this call was in flight"))))
			Eventually(restartResult).Should(Receive(Succeed()))
		})
	})

	When("sixteen prepared roots require directed requests", func() {
		It("runs no more and no fewer than four requests at once", func() {
			maxConcurrencyLog := filepath.Join(GinkgoT().TempDir(), "concurrency.log")
			manager, file := fakeManager(ctx, "target()",
				"-call-hierarchy",
				"-call-hierarchy-roots-count=16",
				"-call-hierarchy-async",
				"-call-hierarchy-delay=50ms",
				"-max-directed-concurrency-log="+maxConcurrencyLog)

			hierarchies, err := manager.CallHierarchy(
				ctx, "fake", file, 1, 1, "incoming")
			Expect(err).NotTo(HaveOccurred())
			Expect(hierarchies).To(HaveLen(16))
			Expect(logLines(maxConcurrencyLog)).To(Equal([]string{"1", "2", "3", "4"}))
		})
	})
})
