package com.example.lib;

/**
 * Lib is a target for cross-file rename eval. It exports a static
 * method `compute` and a regular method `process`, both of which are
 * called from sibling files in other packages.
 */
public class Lib {
    public static int compute(int x) {
        return x * 2;
    }

    public int process(String s) {
        return s.length();
    }
}
