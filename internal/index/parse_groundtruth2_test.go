package index

import (
	"testing"
)

// TestGroundTruth2_Ruby verifies the Ruby parser against subscriber_map.rb.
func TestGroundTruth2_Ruby(t *testing.T) {
	src := []byte(`# frozen_string_literal: true

# :markup: markdown

module ActionCable
  module SubscriptionAdapter
    class SubscriberMap
      def initialize
        @subscribers = Hash.new { |h, k| h[k] = [] }
        @sync = Mutex.new
      end

      def add_subscriber(channel, subscriber, on_success)
        @sync.synchronize do
          new_channel = !@subscribers.key?(channel)

          @subscribers[channel] << subscriber

          if new_channel
            add_channel channel, on_success
          elsif on_success
            on_success.call
          end
        end
      end

      def remove_subscriber(channel, subscriber)
        @sync.synchronize do
          @subscribers[channel].delete(subscriber)

          if @subscribers[channel].empty?
            @subscribers.delete channel
            remove_channel channel
          end
        end
      end

      def broadcast(channel, message)
        list = @sync.synchronize do
          return if !@subscribers.key?(channel)
          @subscribers[channel].dup
        end

        list.each do |subscriber|
          invoke_callback(subscriber, message)
        end
      end

      def add_channel(channel, on_success)
        on_success.call if on_success
      end

      def remove_channel(channel)
      end

      def invoke_callback(callback, message)
        callback.call message
      end
    end
  end
end
`)
	r := ParseRuby(src)
	want := []struct{ typ, name string }{
		{"module", "ActionCable"},
		{"module", "SubscriptionAdapter"},
		{"class", "SubscriberMap"},
		{"method", "initialize"},
		{"method", "add_subscriber"},
		{"method", "remove_subscriber"},
		{"method", "broadcast"},
		{"method", "add_channel"},
		{"method", "remove_channel"},
		{"method", "invoke_callback"},
	}
	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth2_TypeScript verifies the TypeScript parser against
// partialCommandDetectionCapability.ts.
func TestGroundTruth2_TypeScript(t *testing.T) {
	src := []byte(`/*---------------------------------------------------------------------------------------------
 *  Copyright (c) Microsoft Corporation. All rights reserved.
 *  Licensed under the MIT License. See License.txt in the project root for license information.
 *--------------------------------------------------------------------------------------------*/

import { Emitter, Event } from '../../../../base/common/event.js';
import { DisposableStore } from '../../../../base/common/lifecycle.js';
import { IPartialCommandDetectionCapability, TerminalCapability } from './capabilities.js';
import type { IMarker, Terminal } from '@xterm/headless';

const enum Constants {
	/**
	 * The minimum size of the prompt in which to assume the line is a command.
	 */
	MinimumPromptLength = 2
}

/**
 * This capability guesses where commands are based on where the cursor was when enter was pressed.
 * It's very hit or miss but it's often correct and better than nothing.
 */
export class PartialCommandDetectionCapability extends DisposableStore implements IPartialCommandDetectionCapability {
	readonly type = TerminalCapability.PartialCommandDetection;

	private readonly _commands: IMarker[] = [];

	get commands(): readonly IMarker[] { return this._commands; }

	private readonly _onCommandFinished = this.add(new Emitter<IMarker>());
	readonly onCommandFinished = this._onCommandFinished.event;

	constructor(
		private readonly _terminal: Terminal,
		private _onDidExecuteText: Event<void> | undefined
	) {
		super();
		this.add(this._terminal.onData(e => this._onData(e)));
		this.add(this._terminal.parser.registerCsiHandler({ final: 'J' }, params => {
			if (params.length >= 1 && (params[0] === 2 || params[0] === 3)) {
				this._clearCommandsInViewport();
			}
			// We don't want to override xterm.js' default behavior, just augment it
			return false;
		}));
		if (this._onDidExecuteText) {
			this.add(this._onDidExecuteText(() => this._onEnter()));
		}
	}

	private _onData(data: string): void {
		if (data === '\x0d') {
			this._onEnter();
		}
	}

	private _onEnter(): void {
		if (!this._terminal) {
			return;
		}
		if (this._terminal.buffer.active.cursorX >= Constants.MinimumPromptLength) {
			const marker = this._terminal.registerMarker(0);
			if (marker) {
				this._commands.push(marker);
				this._onCommandFinished.fire(marker);
			}
		}
	}

	private _clearCommandsInViewport(): void {
		// Find the number of commands on the tail end of the array that are within the viewport
		let count = 0;
		for (let i = this._commands.length - 1; i >= 0; i--) {
			if (this._commands[i].line < this._terminal.buffer.active.baseY) {
				break;
			}
			count++;
		}
		// Remove them
		this._commands.splice(this._commands.length - count, count);
	}
}
`)
	r := ParseTS(src)
	// File declares:
	//   - `const enum Constants` (TS compile-time enum) → type "enum"
	//   - export class PartialCommandDetectionCapability → type "class"
	//   - get commands() → type "method"
	//   - constructor → type "method"
	//   - private _onData() → type "method"
	//   - private _onEnter() → type "method"
	//   - private _clearCommandsInViewport() → type "method"
	want := []struct{ typ, name string }{
		{"enum", "Constants"},
		{"class", "PartialCommandDetectionCapability"},
		{"method", "commands"},
		{"method", "constructor"},
		{"method", "_onData"},
		{"method", "_onEnter"},
		{"method", "_clearCommandsInViewport"},
	}
	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth2_C verifies the C parser against timekeeping_internal.h.
//
// The header uses #ifdef / #else / #endif for conditional compilation; the
// parser is text-based and does NOT evaluate preprocessor conditions, so
// symbols from both branches are recorded. Concretely:
//
//   - DECLARE_PER_CPU macro call is parsed as a function declaration (sawParen
//     + semicolon path in tryParseDeclaration).
//   - timekeeping_inc_mg_floor_swaps appears in BOTH the #ifdef and #else
//     branches, so it is recorded twice.
//   - extern tk_debug_account_sleep_time() is a function declaration.
//   - The #define tk_debug_account_sleep_time(x) in the #else branch is a
//     preprocessor directive: skipped, no symbol recorded.
func TestGroundTruth2_C(t *testing.T) {
	src := []byte(`/* SPDX-License-Identifier: GPL-2.0 */
#ifndef _TIMEKEEPING_INTERNAL_H
#define _TIMEKEEPING_INTERNAL_H

#include <linux/clocksource.h>
#include <linux/spinlock.h>
#include <linux/time.h>

/*
 * timekeeping debug functions
 */
#ifdef CONFIG_DEBUG_FS

DECLARE_PER_CPU(unsigned long, timekeeping_mg_floor_swaps);

static inline void timekeeping_inc_mg_floor_swaps(void)
{
	this_cpu_inc(timekeeping_mg_floor_swaps);
}

extern void tk_debug_account_sleep_time(const struct timespec64 *t);

#else

#define tk_debug_account_sleep_time(x)

static inline void timekeeping_inc_mg_floor_swaps(void)
{
}

#endif

static inline u64 clocksource_delta(u64 now, u64 last, u64 mask, u64 max_delta)
{
	u64 ret = (now - last) & mask;

	/*
	 * Prevent time going backwards by checking the result against
	 * @max_delta. If greater, return 0.
	 */
	return ret > max_delta ? 0 : ret;
}

/* Semi public for serialization of non timekeeper VDSO updates. */
unsigned long timekeeper_lock_irqsave(void);
void timekeeper_unlock_irqrestore(unsigned long flags);

/* NTP specific interface to access the current seconds value */
long ktime_get_ntp_seconds(unsigned int id);

#endif /* _TIMEKEEPING_INTERNAL_H */
`)
	r := ParseCpp(src)
	want := []struct{ typ, name string }{
		// #ifdef CONFIG_DEBUG_FS branch
		{"function", "DECLARE_PER_CPU"},
		{"function", "timekeeping_inc_mg_floor_swaps"},
		{"function", "tk_debug_account_sleep_time"},
		// #else branch: duplicate static inline (no body variant counts too)
		{"function", "timekeeping_inc_mg_floor_swaps"},
		// After #endif
		{"function", "clocksource_delta"},
		{"function", "timekeeper_lock_irqsave"},
		{"function", "timekeeper_unlock_irqrestore"},
		{"function", "ktime_get_ntp_seconds"},
	}
	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth2_CSharp verifies the C# parser against XmlLocation.cs.
//
// The parser records the enclosing namespace "Microsoft.CodeAnalysis" as a
// "class" (the C# parser uses "class" for namespace blocks), then the actual
// class XmlLocation and all its methods (including two overloads of Create and
// two overloads of Equals).
func TestGroundTruth2_CSharp(t *testing.T) {
	// Note: original file has a UTF-8 BOM; omitted here as the parser handles plain UTF-8.
	src := []byte(`// Licensed to the .NET Foundation under one or more agreements.
// The .NET Foundation licenses this file to you under the MIT license.
// See the LICENSE file in the project root for more information.

using Microsoft.CodeAnalysis.Text;
using System;
using System.Xml.Linq;
using System.Xml;
using System.Diagnostics;

namespace Microsoft.CodeAnalysis
{
    /// <summary>
    /// A program location in an XML file.
    /// </summary>
    internal class XmlLocation : Location, IEquatable<XmlLocation?>
    {
        private readonly FileLinePositionSpan _positionSpan;

        private XmlLocation(string path, int lineNumber, int columnNumber)
        {
            LinePosition start = new LinePosition(lineNumber, columnNumber);
            LinePosition end = new LinePosition(lineNumber, columnNumber + 1);
            _positionSpan = new FileLinePositionSpan(path, start, end);
        }

        public static XmlLocation Create(XmlException exception, string path)
        {
            // Convert to 0-indexed (special case - sometimes 0,0).
            int lineNumber = Math.Max(exception.LineNumber - 1, 0);
            int columnNumber = Math.Max(exception.LinePosition - 1, 0);

            return new XmlLocation(path, lineNumber, columnNumber);
        }

        public static XmlLocation Create(XObject obj, string path)
        {
            IXmlLineInfo xmlLineInfo = obj;
            Debug.Assert(xmlLineInfo.LinePosition != 0);

            // Convert to 0-indexed (special case - sometimes 0,0).
            int lineNumber = Math.Max(xmlLineInfo.LineNumber - 1, 0);
            int columnNumber = Math.Max(xmlLineInfo.LinePosition - 1, 0);

            return new XmlLocation(path, lineNumber, columnNumber);
        }

        public override LocationKind Kind
        {
            get
            {
                return LocationKind.XmlFile;
            }
        }

        public override FileLinePositionSpan GetLineSpan()
        {
            return _positionSpan;
        }

        public bool Equals(XmlLocation? other)
        {
            if (ReferenceEquals(this, other))
            {
                return true;
            }

            return other != null && other._positionSpan.Equals(_positionSpan);
        }

        public override bool Equals(object? obj)
        {
            return this.Equals(obj as XmlLocation);
        }

        public override int GetHashCode()
        {
            return _positionSpan.GetHashCode();
        }
    }
}
`)
	r := ParseCSharp(src)
	want := []struct{ typ, name string }{
		{"class", "Microsoft.CodeAnalysis"},
		{"class", "XmlLocation"},
		{"method", "XmlLocation"},
		{"method", "Create"},
		{"method", "Create"},
		{"method", "Kind"},
		{"method", "GetLineSpan"},
		{"method", "Equals"},
		{"method", "Equals"},
		{"method", "GetHashCode"},
	}
	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth2_Kotlin verifies the Kotlin parser against
// ClassNameCollectionClassBuilderFactory.kt.
//
// The file declares an abstract class at the top level and a private inner
// class. The parser records the outer class methods (handleClashingNames,
// newClassBuilder) and inner class methods (getDelegate, defineClass, done)
// as "function" because they appear outside a class body scope or because the
// Kotlin parser uses "function" for all fun declarations regardless of nesting.
func TestGroundTruth2_Kotlin(t *testing.T) {
	src := []byte(`/*
 * Copyright 2010-2016 JetBrains s.r.o.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package org.jetbrains.kotlin.codegen

import com.intellij.psi.PsiElement
import org.jetbrains.kotlin.resolve.jvm.diagnostics.JvmDeclarationOrigin

abstract class ClassNameCollectionClassBuilderFactory(
        delegate: ClassBuilderFactory
) : DelegatingClassBuilderFactory(delegate) {

    protected abstract fun handleClashingNames(internalName: String, origin: JvmDeclarationOrigin)

    override fun newClassBuilder(origin: JvmDeclarationOrigin): DelegatingClassBuilder {
        return ClassNameCollectionClassBuilder(origin, delegate.newClassBuilder(origin))
    }

    private inner class ClassNameCollectionClassBuilder(
            private val classCreatedFor: JvmDeclarationOrigin,
            internal val _delegate: ClassBuilder
    ) : DelegatingClassBuilder() {

        override fun getDelegate() = _delegate

        private lateinit var classInternalName: String

        override fun defineClass(origin: PsiElement?, version: Int, access: Int, name: String, signature: String?, superName: String, interfaces: Array<out String>) {
            classInternalName = name
            super.defineClass(origin, version, access, name, signature, superName, interfaces)
        }

        override fun done(generateSmapCopyToAnnotation: Boolean) {
            handleClashingNames(classInternalName, classCreatedFor)
            super.done(generateSmapCopyToAnnotation)
        }
    }
}
`)
	r := ParseKotlin(src)
	want := []struct{ typ, name string }{
		{"class", "ClassNameCollectionClassBuilderFactory"},
		{"function", "handleClashingNames"},
		{"function", "newClassBuilder"},
		{"class", "ClassNameCollectionClassBuilder"},
		{"function", "getDelegate"},
		{"function", "defineClass"},
		{"function", "done"},
	}
	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth2_Swift verifies the Swift parser against ErrorSource.swift.
//
// The file declares:
//   - struct ErrorSource with an init method
//   - extension ErrorSource (recorded as "impl") with a static func capture
func TestGroundTruth2_Swift(t *testing.T) {
	// Note: backticks in doc comments are split across raw string literals.
	src := []byte(`/// A source-code location.
public struct ErrorSource: Sendable {
    /// File in which this location exists.
    public var file: String

    /// Function in which this location exists.
    public var function: String

    /// Line number this location belongs to.
    public var line: UInt

    /// Number of characters into the line this location starts at.
    public var column: UInt

    /// Optional start/end range of the source.
    public var range: Range<UInt>?

    /// Creates a new ` + "`" + `SourceLocation` + "`" + `
    public init(
        file: String,
        function: String,
        line: UInt,
        column: UInt,
        range: Range<UInt>? = nil
    ) {
        self.file = file
        self.function = function
        self.line = line
        self.column = column
        self.range = range
    }
}

extension ErrorSource {
    /// Creates a new ` + "`" + `ErrorSource` + "`" + ` for the current call site.
    public static func capture(
        file: String = #fileID,
        function: String = #function,
        line: UInt = #line,
        column: UInt = #column,
        range: Range<UInt>? = nil
    ) -> Self {
        return self.init(
            file: file,
            function: function,
            line: line,
            column: column,
            range: range
        )
    }
}
`)
	r := ParseSwift(src)
	want := []struct{ typ, name string }{
		{"class", "ErrorSource"},
		{"method", "init"},
		{"impl", "ErrorSource"},
		{"function", "capture"},
	}
	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}
