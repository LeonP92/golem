# internal/agentrunner

This module abstracts the execution of one-shot AI agent invocations behind a Runner interface, enabling the orchestration core to dispatch role prompts without coupling to a specific backend. It provides a ClaudeCode adapter that shells out to the `claude` CLI, a Mock adapter for test-time scripting, a BuildPrompt function that wraps role prompts and context data in injection-resistant delimiters, and artifact generators that project neutral role files into Claude Code subagent definitions (.claude/agents/*.md), a merged .claude/settings.json permission allow/deny list, and slash-command skill files (new-ticket, tickets).

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
