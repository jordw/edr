package com.example.iface;

// Service is an interface with a method that ServiceImpl implements.
// Used to verify the interface-method rename limitation: renaming
// ServiceImpl.run() should ALSO rename Service.run() but does not
// (extractive ceiling — needs hierarchy index, Phase 8).
public interface Service {
    String run(String input);
}
