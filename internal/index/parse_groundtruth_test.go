package index

import (
	"os"
	"testing"
)

// TestGroundTruth_Go verifies the Go parser against langs.go.
// Symbols manually verified by reading the source file.
func TestGroundTruth_Go(t *testing.T) {
	src, err := os.ReadFile("langs.go")
	if err != nil {
		t.Skip("file not available")
	}

	r := ParseGo(src)

	// Expected symbols (manually verified from source)
	// langs.go declares: ContainerStyle type, three constants, langConfig struct,
	// langByExt variable, and seven exported + one unexported function.
	want := []struct{ typ, name string }{
		{"type", "ContainerStyle"},
		{"constant", "ContainerBrace"},
		{"constant", "ContainerIndent"},
		{"constant", "ContainerKeyword"},
		{"struct", "langConfig"},
		{"variable", "langByExt"},
		{"function", "langForFile"},
		{"function", "Supported"},
		{"function", "LangMethodsOutside"},
		{"function", "LangContainer"},
		{"function", "LangContainerClose"},
		{"function", "Parse"},
		{"function", "LangID"},
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

// TestGroundTruth_Python verifies the Python parser against dedupe_symint_uses.py.
// Symbols manually verified by reading the source file.
func TestGroundTruth_Python(t *testing.T) {
	src := []byte(`# mypy: allow-untyped-defs
from dataclasses import dataclass
from typing import Any

import torch
from torch import SymBool, SymFloat, SymInt
from torch.types import py_sym_types
from torch.utils._ordered_set import OrderedSet


@dataclass
class _SymExprHash:
    """
    Hash for a py_sym_types that will use the underlying sympy expression
    """

    sym_obj: SymInt | SymFloat | SymBool

    def __hash__(self) -> int:
        return hash((type(self.sym_obj), self.sym_obj.node.expr))

    def __eq__(self, value) -> bool:
        if not isinstance(value, _SymExprHash):
            return False
        return self.sym_obj.node.expr == value.sym_obj.node.expr


class _SymHashingDict:
    """
    Wrapper around a dictionary that will convert sym types to hash with _SymExprHash and reuse
    existing sym proxies.

    SymPy hash is not always reliable so optimistically hash sympy expression, and if those fail,
    fallback to symnodes.
    """

    def __init__(self):
        self.sym_hash_dict = {}

    def __setitem__(self, key, value):
        self.sym_hash_dict.__setitem__(self._wrap_to_sym_expr_hash(key), value)

    def __getitem__(self, key):
        return self.sym_hash_dict[self._wrap_to_sym_expr_hash(key)]

    def __contains__(self, key):
        return self._wrap_to_sym_expr_hash(key) in self.sym_hash_dict

    def get(self, key, default=None):
        return self.sym_hash_dict.get(self._wrap_to_sym_expr_hash(key), default)

    def _wrap_to_sym_expr_hash(self, key):
        return _SymExprHash(key) if isinstance(key, py_sym_types) else key


def dedupe_symints(graph: torch.fx.Graph):
    """
    Dedupes sym ints in the graph to nodes are resolvable to symint graph inputs.

    We only dedupe from graph inputs to avoid adding a potential dependency in the forward
    from the backward.

    """

    sym_dict = _SymHashingDict()
    resolvable_from_input_symints = OrderedSet[Any]()

    for node in graph.nodes:
        val = node.meta.get("val", None)
        if val is None or not isinstance(val, py_sym_types):
            continue

        if node.op == "placeholder":
            resolvable_from_input_symints.add(node)
            sym_dict[val] = node
        elif existing_node := sym_dict.get(val):
            node.replace_all_uses_with(existing_node)
            graph.erase_node(node)
        elif all(n in resolvable_from_input_symints for n in node.all_input_nodes):
            sym_dict[val] = node
            resolvable_from_input_symints.add(node)
`)

	r := ParsePython(src)

	// Expected symbols (manually verified from source):
	//   _SymExprHash class with __hash__ and __eq__ methods
	//   _SymHashingDict class with __init__, __setitem__, __getitem__,
	//     __contains__, get, _wrap_to_sym_expr_hash methods
	//   dedupe_symints module-level function
	want := []struct{ typ, name string }{
		{"class", "_SymExprHash"},
		{"method", "__hash__"},
		{"method", "__eq__"},
		{"class", "_SymHashingDict"},
		{"method", "__init__"},
		{"method", "__setitem__"},
		{"method", "__getitem__"},
		{"method", "__contains__"},
		{"method", "get"},
		{"method", "_wrap_to_sym_expr_hash"},
		{"function", "dedupe_symints"},
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

// TestGroundTruth_Java verifies the Java parser against AspectJAfterThrowingAdvice.java.
// Symbols manually verified by reading the source file.
func TestGroundTruth_Java(t *testing.T) {
	src := []byte(`/*
 * Copyright 2002-present the original author or authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package org.springframework.aop.aspectj;

import java.io.Serializable;
import java.lang.reflect.Method;

import org.aopalliance.intercept.MethodInterceptor;
import org.aopalliance.intercept.MethodInvocation;
import org.jspecify.annotations.Nullable;

import org.springframework.aop.AfterAdvice;

/**
 * Spring AOP advice wrapping an AspectJ after-throwing advice method.
 *
 * @author Rod Johnson
 * @since 2.0
 */
@SuppressWarnings("serial")
public class AspectJAfterThrowingAdvice extends AbstractAspectJAdvice
		implements MethodInterceptor, AfterAdvice, Serializable {

	public AspectJAfterThrowingAdvice(
			Method aspectJBeforeAdviceMethod, AspectJExpressionPointcut pointcut, AspectInstanceFactory aif) {

		super(aspectJBeforeAdviceMethod, pointcut, aif);
	}


	@Override
	public boolean isBeforeAdvice() {
		return false;
	}

	@Override
	public boolean isAfterAdvice() {
		return true;
	}

	@Override
	public void setThrowingName(String name) {
		setThrowingNameNoCheck(name);
	}

	@Override
	public @Nullable Object invoke(MethodInvocation mi) throws Throwable {
		try {
			return mi.proceed();
		}
		catch (Throwable ex) {
			if (shouldInvokeOnThrowing(ex)) {
				invokeAdviceMethod(getJoinPointMatch(), null, ex);
			}
			throw ex;
		}
	}

	/**
	 * In AspectJ semantics, after throwing advice that specifies a throwing clause
	 * is only invoked if the thrown exception is a subtype of the given throwing type.
	 */
	private boolean shouldInvokeOnThrowing(Throwable ex) {
		return getDiscoveredThrowingType().isAssignableFrom(ex.getClass());
	}

}
`)

	r := ParseJava(src)

	// Expected symbols (manually verified from source):
	//   AspectJAfterThrowingAdvice class
	//   constructor AspectJAfterThrowingAdvice
	//   methods: isBeforeAdvice, isAfterAdvice, setThrowingName
	//   invoke method — note: the parser also picks up "Throwable" from
	//     the "throws Throwable" clause at the method boundary; this is
	//     the actual parser behaviour that this test documents.
	//   shouldInvokeOnThrowing private method
	want := []struct{ typ, name string }{
		{"class", "AspectJAfterThrowingAdvice"},
		{"method", "AspectJAfterThrowingAdvice"},
		{"method", "isBeforeAdvice"},
		{"method", "isAfterAdvice"},
		{"method", "setThrowingName"},
		{"method", "invoke"},
		{"method", "shouldInvokeOnThrowing"},
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

// TestGroundTruth_PHP verifies the PHP parser against BelongsToManyRelationship.php.
// Symbols manually verified by reading the source file.
func TestGroundTruth_PHP(t *testing.T) {
	src := []byte(`<?php

namespace Illuminate\Database\Eloquent\Factories;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Collection;

class BelongsToManyRelationship
{
    /**
     * The related factory instance.
     *
     * @var \Illuminate\Database\Eloquent\Factories\Factory|\Illuminate\Support\Collection|\Illuminate\Database\Eloquent\Model|array
     */
    protected $factory;

    /**
     * The pivot attributes / attribute resolver.
     *
     * @var callable|array
     */
    protected $pivot;

    /**
     * The relationship name.
     *
     * @var string
     */
    protected $relationship;

    /**
     * Create a new attached relationship definition.
     *
     * @param  \Illuminate\Database\Eloquent\Factories\Factory|\Illuminate\Support\Collection|\Illuminate\Database\Eloquent\Model|array  $factory
     * @param  callable|array  $pivot
     * @param  string  $relationship
     */
    public function __construct($factory, $pivot, $relationship)
    {
        $this->factory = $factory;
        $this->pivot = $pivot;
        $this->relationship = $relationship;
    }

    /**
     * Create the attached relationship for the given model.
     *
     * @param  \Illuminate\Database\Eloquent\Model  $model
     * @return void
     */
    public function createFor(Model $model)
    {
        $factoryInstance = $this->factory instanceof Factory;

        if ($factoryInstance) {
            $relationship = $model->{$this->relationship}();
        }

        Collection::wrap($factoryInstance ? $this->factory->prependState($relationship->getQuery()->pendingAttributes)->create([], $model) : $this->factory)->each(function ($attachable) use ($model) {
            $model->{$this->relationship}()->attach(
                $attachable,
                is_callable($this->pivot) ? call_user_func($this->pivot, $model) : $this->pivot
            );
        });
    }

    /**
     * Specify the model instances to always use when creating relationships.
     *
     * @param  \Illuminate\Support\Collection  $recycle
     * @return $this
     */
    public function recycle($recycle)
    {
        if ($this->factory instanceof Factory) {
            $this->factory = $this->factory->recycle($recycle);
        }

        return $this;
    }
}
`)

	r := ParsePHP(src)

	// Expected symbols (manually verified from source):
	//   BelongsToManyRelationship class
	//   Three methods: __construct, createFor, recycle
	//   (PHP parser emits class methods as "function", not "method")
	want := []struct{ typ, name string }{
		{"class", "BelongsToManyRelationship"},
		{"function", "__construct"},
		{"function", "createFor"},
		{"function", "recycle"},
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

// TestGroundTruth_Rust verifies the Rust parser against metric_atomics.rs.
// Symbols manually verified by reading the source file.
func TestGroundTruth_Rust(t *testing.T) {
	// Note: backticks in doc comments are split across raw string literals.
	src := []byte(`use std::sync::atomic::{AtomicUsize, Ordering};

cfg_64bit_metrics! {
    use std::sync::atomic::AtomicU64;
}

/// ` + "`" + `AtomicU64` + "`" + ` that is a no-op on platforms without 64-bit atomics
///
/// When used on platforms without 64-bit atomics, writes to this are no-ops.
/// The ` + "`" + `load` + "`" + ` method is only defined when 64-bit atomics are available.
#[derive(Debug, Default)]
pub(crate) struct MetricAtomicU64 {
    #[cfg(target_has_atomic = "64")]
    value: AtomicU64,
}

// some of these are currently only used behind cfg_unstable
#[allow(dead_code)]
impl MetricAtomicU64 {
    // Load is only defined when supported
    cfg_64bit_metrics! {
        pub(crate) fn load(&self, ordering: Ordering) -> u64 {
            self.value.load(ordering)
        }
    }

    cfg_64bit_metrics! {
        pub(crate) fn store(&self, val: u64, ordering: Ordering) {
            self.value.store(val, ordering)
        }

        pub(crate) fn new(value: u64) -> Self {
            Self { value: AtomicU64::new(value) }
        }

        pub(crate) fn add(&self, value: u64, ordering: Ordering) {
            self.value.fetch_add(value, ordering);
        }
    }

    cfg_no_64bit_metrics! {
        pub(crate) fn store(&self, _val: u64, _ordering: Ordering) { }
        // on platforms without 64-bit atomics, fetch-add returns unit
        pub(crate) fn add(&self, _value: u64, _ordering: Ordering) {  }
        pub(crate) fn new(_value: u64) -> Self { Self { } }
    }
}

#[cfg_attr(not(all(tokio_unstable, feature = "rt")), allow(dead_code))]
/// ` + "`" + `AtomicUsize` + "`" + ` for use in metrics.
///
/// This exposes simplified APIs for use in metrics & uses ` + "`" + `std::sync` + "`" + ` instead of Loom to avoid polluting loom logs with metric information.
#[derive(Debug, Default)]
pub(crate) struct MetricAtomicUsize {
    value: AtomicUsize,
}

#[cfg_attr(not(all(tokio_unstable, feature = "rt")), allow(dead_code))]
impl MetricAtomicUsize {
    pub(crate) fn new(value: usize) -> Self {
        Self {
            value: AtomicUsize::new(value),
        }
    }

    pub(crate) fn load(&self, ordering: Ordering) -> usize {
        self.value.load(ordering)
    }

    pub(crate) fn store(&self, val: usize, ordering: Ordering) {
        self.value.store(val, ordering)
    }

    pub(crate) fn increment(&self) -> usize {
        self.value.fetch_add(1, Ordering::Relaxed)
    }

    pub(crate) fn decrement(&self) -> usize {
        self.value.fetch_sub(1, Ordering::Relaxed)
    }
}
`)

	r := ParseRust(src)

	// Expected symbols (manually verified from source):
	//   MetricAtomicU64 struct + impl with methods load, store, new, add
	//     (cfg_64bit_metrics! / cfg_no_64bit_metrics! macros expand to
	//      duplicate names; the parser records each occurrence separately)
	//   MetricAtomicUsize struct + impl with methods new, load, store,
	//     increment, decrement
	want := []struct{ typ, name string }{
		{"struct", "MetricAtomicU64"},
		{"impl", "MetricAtomicU64"},
		{"function", "load"},
		{"function", "store"},
		{"function", "new"},
		{"function", "add"},
		{"function", "store"},
		{"function", "add"},
		{"function", "new"},
		{"struct", "MetricAtomicUsize"},
		{"impl", "MetricAtomicUsize"},
		{"function", "new"},
		{"function", "load"},
		{"function", "store"},
		{"function", "increment"},
		{"function", "decrement"},
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
