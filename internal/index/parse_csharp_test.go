package index

import "testing"

func TestParseCSharp_Fixture(t *testing.T) {
	src := []byte(`using System;
using System.Collections.Generic;
using static System.Math;

namespace MyApp.Models
{
    public class Widget<T> : Base, IWidget where T : IComparable
    {
        private readonly string _name;

        public Widget(string name)
        {
            _name = name;
        }

        public string Name { get; set; }

        public async Task<List<T>> GetItems(int count)
        {
            var s = "class Fake {}";
            return new List<T>();
        }

        public static Widget<T> Create(string name) => new(name);

        public abstract void DoWork();

        public interface ICallback
        {
            void OnComplete(string result);
        }

        public enum Status
        {
            Active,
            Inactive
        }

        public record Point(double X, double Y);

        private class Inner
        {
            void Helper() {}
        }
    }

    public struct Vector3
    {
        public float X, Y, Z;
    }
}

namespace FileScopedNs;

public class TopLevel
{
    public void Run() {}
}
`)
	r := ParseCSharp(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	found := map[string]bool{}
	for _, s := range r.Symbols {
		found[s.Type+":"+s.Name] = true
	}
	mustHave := []string{
		"class:Widget",
		"method:Widget",
		"method:GetItems",
		"method:Create",
		"method:DoWork",
		"class:ICallback",
		"method:OnComplete",
		"class:Status",
		"class:Point",
		"class:Inner",
		"method:Helper",
		"class:Vector3",
		"class:TopLevel",
		"method:Run",
	}
	for _, want := range mustHave {
		if !found[want] {
			t.Errorf("missing symbol: %s", want)
		}
	}
	if found["class:Fake"] {
		t.Error("spurious Fake from string")
	}

	wantImps := []string{"System", "System.Collections.Generic", "System.Math"}
	if len(r.Imports) < len(wantImps) {
		t.Errorf("got %d imports, want at least %d", len(r.Imports), len(wantImps))
	}
}
