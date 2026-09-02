# internal/agentrunner

This module abstracts the execution of one-shot AI agent invocations behind a Runner interface, enabling the orchestration core to dispatch role prompts without coupling to a specific backend. It provides a ClaudeCode adapter that shells out to the `claude` CLI, a Mock adapter for test-time scripting, a BuildPrompt function that wraps role prompts and context data in injection-resistant delimiters, and artifact generators that project neutral role files into Claude Code subagent definitions and slash-command skill files.

## Functions

- func GenerateClaudeCodeArtifacts(repoRoot string, roleContent map[string]string) error — writes Claude Code subagent .md files and merges permissions into .claude/settings.json
- func GenerateClaudeCodeCommands(repoRoot string) error — writes golem skill files (new-ticket.md, tickets.md) into .claude/commands/golem/
- func BuildPrompt(ctx Context) string — assembles a role prompt with log and diff wrapped in injection-resistant delimiters
- func NewMock() *Mock — constructs a scripted Mock runner for use in tests
- func (c ClaudeCode) RunAgent(role string, ctx Context) (Result, error) — dispatches a one-shot `claude --print` invocation with the built prompt
- func (c ClaudeCode) WorktreeSetup(worktreePath string) error — writes a minimal .claude/settings.json with the allow list into a worktree
- func (m *Mock) RunAgent(role string, ctx Context) (Result, error) — returns the next scripted Result queued for the given role
- func (m *Mock) ScriptResponse(role string, result Result) — enqueues a scripted Result for a given role
- func (m *Mock) WorktreeSetup(_ string) error — no-op worktree setup for the mock runner

## Types

- Context — all inputs needed for a one-shot role invocation: log slice, diff, and role prompt
- Result — output of a role invocation: text output and model identifier
- Runner — interface satisfied by ClaudeCode and Mock; defines RunAgent and WorktreeSetup
- ClaudeCode — Claude CLI-backed Runner with repo root and optional model override
- Mock — scripted test double for Runner with a per-role queue of Results

## Imports

github.com/leonpham/golem/internal/blog
