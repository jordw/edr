package com.example.other;

// Other deliberately defines a `compute` with the same name as
// com.example.lib.Lib.compute to test cross-class disambiguation.
// A rename of Lib.compute MUST NOT touch Other.compute.
public class Other {
    public static String compute(String s) {
        return s + s;
    }
}
