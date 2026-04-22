package com.example.iface;

public class ServiceUser {
    public String use() {
        Service s = new ServiceImpl();
        return s.run("hello");
    }
}
