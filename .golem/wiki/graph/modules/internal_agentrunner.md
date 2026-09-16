# internal/agentrunner

This module abstracts the execution of one-shot AI agent invocations behind a Runner interface, enabling the orchestration core to dispatch role prompts without coupling to a specific backend. It provides a ClaudeCode adapter that shells out to the `claude` CLI and generates Claude Code projection artifacts (subagent definitions under .claude/agents, a merged .claude/settings.json permission allow/deny list, and slash-command skill files for new-ticket and tickets workflows), a Mock adapter for scripting deterministic test responses, and a BuildPrompt function that wraps role prompts and log/diff context in explicit data-not-instruction delimiters to raise the bar against prompt injection from log or diff content.

## Functions

- GenerateClaudeCodeArtifacts
- GenerateClaudeCodeCommands
- WorktreeSetup
- RunAgent
- TestGenerateClaudeCodeArtifactsWritesAgentFiles
- TestGenerateClaudeCodeCommandsWritesNewTicketSkill
- TestGenerateClaudeCodeArtifactsWritesSettingsWithDenyList
- TestGenerateClaudeCodeArtifactsMergesExistingSettings
- TestWorktreeSetupWritesSettingsWithDenyList
- TestRunAgentInvokesClaudeCLI
- NewMock
- ScriptResponse
- TestMockReturnsScriptedResponse
- TestMockErrorsOnUnscriptedRole
- TestMockPopsQueuedResponsesInOrder
- BuildPrompt
- TestBuildPromptIncludesRolePromptLogAndDiff
- TestBuildPromptLabelsContentAsDataNotInstructions

## Types

- ClaudeCode
- Mock
- Context
- Result
- Runner

## Imports

bytes, encoding/json, fmt, os, os/exec, path/filepath, strings, testing, github.com/leonp92/golem/internal/blog
