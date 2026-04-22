package com.example.caller;

import com.example.lib.Lib;
import com.example.other.Other;

/**
 * Caller exercises both Lib and Other. A correct rename of
 * Lib.compute must rewrite Lib.compute call sites here while
 * leaving Other.compute alone. A correct rename of Lib.process
 * must rewrite Lib.process method calls.
 */
public class Caller {
    public int useStatic() {
        return Lib.compute(5) + Other.compute("x").length();
    }

    public int useInstance() {
        Lib lib = new Lib();
        return lib.process("hello");
    }
}
