# Backend Development Guidelines

> Best practices for backend development in this project.

---

## Overview

This directory contains guidelines for backend development. Fill in each file with your project's specific conventions.

---

## Guidelines Index

| Guide | Description | Status |
|-------|-------------|--------|
| [Go Conventions](./go-conventions.md) | Package layout, status-string contract, probe judgment rules, JSONL discipline, scheduling gotchas, server security contracts | **Filled** (from MVP task 10-08) |
| [Directory Structure](./directory-structure.md) | Module organization and file layout | To fill |
| [Database Guidelines](./database-guidelines.md) | ORM patterns, queries, migrations | To fill (storage is JSONL; see go-conventions.md) |
| [Error Handling](./error-handling.md) | Error types, handling strategies | See go-conventions.md §Probe judgment rules |
| [Quality Guidelines](./quality-guidelines.md) | Code standards, forbidden patterns | See go-conventions.md §Wrong vs Correct |
| [Logging Guidelines](./logging-guidelines.md) | Structured logging, log levels | To fill |

---

## How to Fill These Guidelines

For each guideline file:

1. Document your project's **actual conventions** (not ideals)
2. Include **code examples** from your codebase
3. List **forbidden patterns** and why
4. Add **common mistakes** your team has made

The goal is to help AI assistants and new team members understand how YOUR project works.

---

**Language**: All documentation should be written in **English**.
