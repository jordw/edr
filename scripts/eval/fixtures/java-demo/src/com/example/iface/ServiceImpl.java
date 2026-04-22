package com.example.iface;

public class ServiceImpl implements Service {
    @Override
    public String run(String input) {
        return input.toUpperCase();
    }
}
