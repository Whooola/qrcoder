# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

QRCoder is a Windows desktop QR code utility that runs as a system-tray application (no install required). It supports Windows 7 SP1+.

**Core features (from spec):**
- **Generate QR code:** Select text in any app, double-tap Ctrl → popup showing QR code for the selected text. Truncates with warning if text exceeds QR capacity.
- **Scan QR code:** With no text selected, double-tap Ctrl → opens camera window. Green guide frame for alignment. Results copyable to clipboard; URLs become clickable links.

**Design constraints:** Portable (no installer), small footprint, local-only (no data upload), system-wide hotkey (double-Ctrl).

## Technology Considerations

The spec calls for a native-feeling Windows tray app that is small and portable. Likely technology paths:

- **Go + Win32 API / systray library** — produces a single static binary, good for "no install, small size."
- **Rust + Windows crate** — similar single-binary advantage with stronger safety guarantees.
- **C#/.NET** — easiest Windows API integration but requires the runtime (likely already present on Win7 SP1+ though version constraints apply).
- **C++ / Win32** — smallest possible binary, most control, highest development cost.

Key technical challenges to plan for:
- Global hotkey registration (double-Ctrl detection without interfering with normal Ctrl usage)
- Screen selection capture across arbitrary applications (UI Automation / COM)
- Camera access and QR scanning (DirectShow / Windows.Media.Capture)
- QR code generation (pure code library, no network dependency)
- System tray with popup menus and overlay windows

## Current State

This repository currently contains only the product specification (`qrcoder.txt`). No code, build system, or dependencies exist yet. The first implementation step is to choose a technology stack and bootstrap the project.
